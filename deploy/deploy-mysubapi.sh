#!/usr/bin/env bash
# Run on the Linux server after uploading an immutable release directory.
set -euo pipefail
umask 077

die() { printf 'Deployment error: %s\n' "$*" >&2; exit 1; }
valid_release() { [[ $1 =~ ^[0-9a-f]{40}-[0-9]+-[0-9]+$ ]]; }
valid_image() { [[ $1 =~ ^ghcr\.io/kenandkent/sub2api@sha256:[0-9a-f]{64}$ ]]; }

[[ $# -eq 3 || $# -eq 4 ]] || die "usage: $0 DEPLOY_ROOT RELEASE_ID IMAGE_DIGEST [GHCR_USER]"
deploy_root=$1
release=$2
image=$3
registry_user=${4:-}
[[ $deploy_root =~ ^/[a-zA-Z0-9_./-]+$ && $deploy_root != / ]] || die "invalid absolute deployment path"
case "/$deploy_root/" in */../*|*/./*) die "deployment path must not contain dot segments";; esac
valid_release "$release" || die "release ID must be SHA-RUN_ID-RUN_ATTEMPT"
valid_image "$image" || die "expected ghcr.io/kenandkent/sub2api@sha256:<64 hex characters>"
[[ -s "$deploy_root/.env" ]] || die "configure the private $deploy_root/.env before enabling deployment"
release_dir="$deploy_root/releases/$release"
for filename in docker-compose.local.yml docker-compose.production.yml; do
    [[ -f "$release_dir/$filename" ]] || die "release is missing $filename"
done
retry_delay=${DEPLOY_RETRY_DELAY_SECONDS:-5}
[[ $retry_delay =~ ^[0-9]+$ ]] || die "invalid retry delay"
pull_timeout=${DEPLOY_PULL_TIMEOUT_SECONDS:-180}
[[ $pull_timeout =~ ^[0-9]+$ ]] && (( pull_timeout > 0 && pull_timeout <= 900 )) ||
    die "invalid image download timeout"
command -v timeout >/dev/null || die "the Linux timeout utility is required"
mkdir "$deploy_root/.deploy-lock" 2>/dev/null || die "another deployment holds .deploy-lock; inspect it before retrying"
auth_dir=
backup_tmp=
state_tmp=
cleanup() {
    if [[ -n $auth_dir ]]; then rm -rf -- "$auth_dir"; fi
    if [[ -n $backup_tmp ]]; then rm -f -- "$backup_tmp"; fi
    if [[ -n $state_tmp ]]; then rm -f -- "$state_tmp"; fi
    rmdir "$deploy_root/.deploy-lock"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM

version=$(docker compose version --short)
version=${version#v}
IFS=. read -r major minor patch <<< "$version"
patch=${patch%%[-+]*}
[[ $major =~ ^[0-9]+$ && $minor =~ ^[0-9]+$ && $patch =~ ^[0-9]+$ ]] || die "unrecognized Compose version"
if (( major < 2 || (major == 2 && minor < 24) || (major == 2 && minor == 24 && patch < 4) )); then
    die "Docker Compose >=2.24.4 is required"
fi

if [[ -n $registry_user ]]; then
    auth_dir=$(mktemp -d "$deploy_root/.registry-auth.XXXXXX")
    export DOCKER_CONFIG="$auth_dir"
    # The short-lived job token is read from SSH stdin, never from argv.
    docker login ghcr.io --username "$registry_user" --password-stdin >/dev/null
fi

compose_for() {
    local selected_release=$1 selected_image=$2
    shift 2
    local command=(docker compose \
        --project-name mysubapi --project-directory "$deploy_root" \
        --env-file "$deploy_root/.env" \
        -f "$deploy_root/releases/$selected_release/docker-compose.local.yml" \
        -f "$deploy_root/releases/$selected_release/docker-compose.production.yml")
    if [[ $1 == pull ]]; then
        SUB2API_IMAGE="$selected_image" timeout -k 10 "$pull_timeout" "${command[@]}" "$@"
    else
        SUB2API_IMAGE="$selected_image" "${command[@]}" "$@"
    fi
}

retry() {
    local attempt
    for attempt in 1 2 3; do
        if "$@"; then return 0; fi
        printf 'Image download failed (attempt %s/3).\n' "$attempt" >&2
        if [[ $attempt != 3 ]]; then sleep "$retry_delay"; fi
    done
    return 1
}

probe() {
    local selected_release=$1 selected_image=$2
    compose_for "$selected_release" "$selected_image" exec -T sub2api \
        wget -q -T 10 -O - http://127.0.0.1:8080/setup/status |
        grep -Eq '"needs_setup"[[:space:]]*:[[:space:]]*false' &&
    compose_for "$selected_release" "$selected_image" exec -T sub2api \
        wget -q -T 10 -O - http://127.0.0.1:8080/api/v1/settings/public |
        grep -Eq '"site_name"[[:space:]]*:[[:space:]]*"[^"]+"'
}

apply_app() {
    local selected_release=$1 selected_image=$2
    compose_for "$selected_release" "$selected_image" up -d --no-build --pull never \
        --no-deps --wait --wait-timeout 300 sub2api &&
    probe "$selected_release" "$selected_image"
}

previous_release=
previous_image=
if [[ -f "$deploy_root/current-deployment" ]]; then
    read -r previous_release previous_image < "$deploy_root/current-deployment"
    valid_release "$previous_release" && valid_image "$previous_image" || die "invalid previous deployment state"
    [[ -f "$deploy_root/releases/$previous_release/docker-compose.production.yml" ]] ||
        die "previous release files are missing"
fi

compose_for "$release" "$image" config --quiet
# Dependency images are only fetched when absent; application images are pinned.
retry compose_for "$release" "$image" pull --policy missing postgres redis
retry compose_for "$release" "$image" pull sub2api
compose_for "$release" "$image" up -d --no-build --pull never --wait --wait-timeout 300 postgres redis
compose_for "$release" "$image" exec -T postgres sh -c \
    'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atqc "SELECT 1"' >/dev/null

mkdir -p "$deploy_root/backups"
backup="$deploy_root/backups/$(date -u +%Y%m%dT%H%M%SZ)-$release.dump"
backup_tmp=$(mktemp "$deploy_root/backups/.dump.XXXXXX")
if ! compose_for "$release" "$image" exec -T postgres sh -c \
    'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$backup_tmp"; then
    die "database backup failed; application was not replaced"
fi
[[ -s $backup_tmp ]] || die "database backup is empty; application was not replaced"
mv -- "$backup_tmp" "$backup"
backup_tmp=
printf 'Pre-update database backup: %s\n' "$backup"

if ! apply_app "$release" "$image"; then
    printf 'Application deployment failed. Database migrations are NOT automatically reversed.\n' >&2
    if [[ -n $previous_release ]]; then
        printf 'Attempting application restore using previous release %s.\n' "$previous_release" >&2
        if apply_app "$previous_release" "$previous_image"; then
            printf 'Previous application restored; verify migration compatibility before retrying.\n' >&2
        else
            printf 'Application restore also failed; inspect the server and retained database backup.\n' >&2
        fi
    fi
    exit 1
fi

state_tmp=$(mktemp "$deploy_root/.current-deployment.XXXXXX")
printf '%s %s\n' "$release" "$image" > "$state_tmp"
mv -- "$state_tmp" "$deploy_root/current-deployment"
state_tmp=
printf 'Deployment verified: %s %s\n' "$release" "$image"
