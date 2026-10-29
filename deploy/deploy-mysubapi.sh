#!/usr/bin/env bash
# Run on the Linux server after uploading an immutable release directory.
set -euo pipefail
umask 077

die() { printf 'Deployment error: %s\n' "$*" >&2; exit 1; }
valid_release() { [[ $1 =~ ^[0-9a-f]{40}-[0-9]+-[0-9]+$ ]]; }
valid_image() { [[ $1 =~ ^ghcr\.io/kenandkent/sub2api@sha256:[0-9a-f]{64}$ ]]; }
valid_worker() { [[ $1 =~ ^ghcr\.io/kenandkent/sub2api-opencode-free-worker@sha256:[0-9a-f]{64}$ ]]; }

[[ $# -ge 3 && $# -le 5 ]] || die "usage: $0 DEPLOY_ROOT RELEASE_ID IMAGE_DIGEST [GHCR_USER] [WORKER_DIGEST]"
deploy_root=$1
release=$2
image=$3
registry_user=${4:-}
worker=${5:--}
[[ -n $worker ]] || worker=-
[[ $deploy_root =~ ^/[a-zA-Z0-9_./-]+$ && $deploy_root != / ]] || die "invalid absolute deployment path"
case "/$deploy_root/" in */../*|*/./*) die "deployment path must not contain dot segments";; esac
valid_release "$release" || die "release ID must be SHA-RUN_ID-RUN_ATTEMPT"
valid_image "$image" || die "expected ghcr.io/kenandkent/sub2api@sha256:<64 hex characters>"
[[ $worker == - ]] || valid_worker "$worker" || die "invalid worker image digest"
[[ -s "$deploy_root/.env" ]] || die "configure the private $deploy_root/.env before enabling deployment"
release_dir="$deploy_root/releases/$release"
for filename in docker-compose.local.yml docker-compose.production.yml; do
    [[ -f "$release_dir/$filename" ]] || die "release is missing $filename"
done
if [[ $worker != - ]]; then
    [[ -f "$release_dir/docker-compose.opencode-free.production.yml" ]] || die "release is missing worker Compose file"
fi
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

# A temporary Docker config has no stored contexts. Fail closed before switching
# configs so commands cannot silently operate on a different Docker daemon.
[[ $(docker context show) == default && -z ${DOCKER_HOST:-} && ${DOCKER_CONTEXT:-default} == default ]] ||
    die "only the default local Docker context is supported"
if [[ -n $registry_user ]]; then
    original_config=${DOCKER_CONFIG:-$HOME/.docker}
    auth_dir=$(mktemp -d "$deploy_root/.registry-auth.XXXXXX")
    if [[ -d "$original_config/cli-plugins" ]]; then
        ln -s -- "$original_config/cli-plugins" "$auth_dir/cli-plugins"
    fi
    export DOCKER_CONFIG="$auth_dir"
    # The short-lived job token is read from SSH stdin, never from argv.
    docker login ghcr.io --username "$registry_user" --password-stdin >/dev/null
fi

compose_for() {
    local selected_release=$1 selected_image=$2 selected_worker=$3
    shift 3
    local command=(docker compose \
        --project-name mysubapi --project-directory "$deploy_root" \
        --env-file "$deploy_root/.env" \
        -f "$deploy_root/releases/$selected_release/docker-compose.local.yml" \
        -f "$deploy_root/releases/$selected_release/docker-compose.production.yml")
    if [[ $selected_worker != - ]]; then
        command+=(-f "$deploy_root/releases/$selected_release/docker-compose.opencode-free.production.yml")
    fi
    if [[ $1 == pull ]]; then
        SUB2API_IMAGE="$selected_image" OPENCODE_FREE_WORKER_IMAGE="$selected_worker" timeout -k 10 "$pull_timeout" "${command[@]}" "$@"
    else
        SUB2API_IMAGE="$selected_image" OPENCODE_FREE_WORKER_IMAGE="$selected_worker" "${command[@]}" "$@"
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
    local selected_release=$1 selected_image=$2 selected_worker=$3
    compose_for "$selected_release" "$selected_image" "$selected_worker" exec -T sub2api \
        wget -q -T 10 -O - http://127.0.0.1:8080/setup/status |
        grep -Eq '"needs_setup"[[:space:]]*:[[:space:]]*false' &&
    compose_for "$selected_release" "$selected_image" "$selected_worker" exec -T sub2api \
        wget -q -T 10 -O - http://127.0.0.1:8080/api/v1/settings/public |
        grep -Eq '"site_name"[[:space:]]*:[[:space:]]*"[^"]+"'
}

apply_app() {
    local selected_release=$1 selected_image=$2 selected_worker=$3
    compose_for "$selected_release" "$selected_image" "$selected_worker" up -d --no-build --pull never \
        --no-deps --wait --wait-timeout 300 sub2api &&
    probe "$selected_release" "$selected_image" "$selected_worker"
}

apply_worker() {
    local selected_release=$1 selected_image=$2 selected_worker=$3
    compose_for "$selected_release" "$selected_image" "$selected_worker" up -d --no-build --pull never \
        --no-deps --force-recreate --wait --wait-timeout 300 opencode-free-worker &&
    compose_for "$selected_release" "$selected_image" "$selected_worker" exec -T sub2api \
        sh -c 'printf "%s" "${OPENCODE_FREE_BUN_TOKEN-}"' |
    compose_for "$selected_release" "$selected_image" "$selected_worker" exec -T opencode-free-worker \
        bun -e 'const token = await Bun.stdin.text(); for (const k of Object.keys(process.env)) if (/^(http_proxy|https_proxy|all_proxy|no_proxy)$/i.test(k)) delete process.env[k]; fetch("http://127.0.0.1:19090/v1/transport/chat", {method: "POST", headers: {authorization: "Bearer " + token}, body: "{}", signal: AbortSignal.timeout(10000)}).then(r => { if (r.status !== 400 || r.headers.get("x-sub2api-dispatched") !== "false") process.exit(1) }).catch(() => process.exit(1))'
}

remove_worker() {
    # Compose resolves required interpolation even for rm; no service starts.
    OPENCODE_FREE_BUN_TOKEN=unused-for-removal-000000000000000 compose_for "$1" "$2" "$3" rm -sf opencode-free-worker
}

previous_release=
previous_image=
previous_worker=-
if [[ -f "$deploy_root/current-deployment" ]]; then
    # Read every field, rejecting extra fields and multiline state.
    mapfile -t state_lines < "$deploy_root/current-deployment"
    [[ ${#state_lines[@]} == 1 ]] || die "invalid previous deployment state"
    read -r -a state_fields <<< "${state_lines[0]}"
    [[ ${#state_fields[@]} == 2 || ${#state_fields[@]} == 3 ]] || die "invalid previous deployment state"
    previous_release=${state_fields[0]}
    previous_image=${state_fields[1]}
    previous_worker=${state_fields[2]:--}
    valid_release "$previous_release" && valid_image "$previous_image" || die "invalid previous deployment state"
    [[ $previous_worker == - ]] || valid_worker "$previous_worker" || die "invalid previous worker state"
    [[ -f "$deploy_root/releases/$previous_release/docker-compose.local.yml" &&
       -f "$deploy_root/releases/$previous_release/docker-compose.production.yml" ]] || die "previous release files are missing"
    if [[ $previous_worker != - ]]; then
        [[ -f "$deploy_root/releases/$previous_release/docker-compose.opencode-free.production.yml" ]] || die "previous worker Compose file is missing"
    fi
fi

validate_worker() {
    local selected_release=$1 selected_image=$2 selected_worker=$3
    # Compose parses the private env file; never source it or print its contents.
    # Locale C makes awk count bytes, matching the backend's token validation.
    if ! compose_for "$selected_release" "$selected_image" "$selected_worker" config --environment |
        LC_ALL=C awk 'index($0, "OPENCODE_FREE_BUN_TOKEN=") == 1 { token = substr($0, 25); found++ } END { exit !(found == 1 && length(token) >= 32 && token ~ /^[!-~]+$/) }'; then
        die "OPENCODE_FREE_BUN_TOKEN must be at least 32 printable ASCII characters without spaces"
    fi
    compose_for "$selected_release" "$selected_image" "$selected_worker" config --quiet
}

if [[ $previous_worker != - ]]; then
    validate_worker "$previous_release" "$previous_image" "$previous_worker"
fi
if [[ $worker != - ]]; then
    validate_worker "$release" "$image" "$worker"
else
    compose_for "$release" "$image" "$worker" config --quiet
fi

# Dependency images are only fetched when absent; application images are pinned.
retry compose_for "$release" "$image" "$worker" pull --policy missing postgres redis
retry compose_for "$release" "$image" "$worker" pull sub2api
if [[ $worker != - ]]; then
    retry compose_for "$release" "$image" "$worker" pull opencode-free-worker
fi
compose_for "$release" "$image" "$worker" up -d --no-build --pull never --wait --wait-timeout 300 postgres redis
compose_for "$release" "$image" "$worker" exec -T postgres sh -c \
    'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atqc "SELECT 1"' >/dev/null

mkdir -p "$deploy_root/backups"
backup="$deploy_root/backups/$(date -u +%Y%m%dT%H%M%SZ)-$release.dump"
backup_tmp=$(mktemp "$deploy_root/backups/.dump.XXXXXX")
if ! compose_for "$release" "$image" "$worker" exec -T postgres sh -c \
    'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$backup_tmp"; then
    die "database backup failed; application was not replaced"
fi
[[ -s $backup_tmp ]] || die "database backup is empty; application was not replaced"
mv -- "$backup_tmp" "$backup"
backup_tmp=
printf 'Pre-update database backup: %s\n' "$backup"

apply_release() {
    local selected_release=$1 selected_image=$2 selected_worker=$3 remove_release=$4 remove_image=$5 remove_image_worker=$6
    apply_app "$selected_release" "$selected_image" "$selected_worker" || return 1
    if [[ $selected_worker != - ]]; then
        apply_worker "$selected_release" "$selected_image" "$selected_worker"
    elif [[ $remove_image_worker != - ]]; then
        remove_worker "$remove_release" "$remove_image" "$remove_image_worker"
    fi
}

if ! apply_release "$release" "$image" "$worker" "$previous_release" "$previous_image" "$previous_worker"; then
    printf 'Application or worker deployment failed. Database migrations are NOT automatically reversed.\n' >&2
    if [[ -n $previous_release ]]; then
        printf 'Attempting application restore using previous release %s.\n' "$previous_release" >&2
        if apply_release "$previous_release" "$previous_image" "$previous_worker" "$release" "$image" "$worker"; then
            printf 'Previous application restored; verify migration compatibility before retrying.\n' >&2
        else
            printf 'Application restore also failed; inspect the server and retained database backup.\n' >&2
        fi
    fi
    exit 1
fi

state_tmp=$(mktemp "$deploy_root/.current-deployment.XXXXXX")
printf '%s %s %s\n' "$release" "$image" "$worker" > "$state_tmp"
mv -- "$state_tmp" "$deploy_root/current-deployment"
state_tmp=
printf 'Deployment verified: %s %s %s\n' "$release" "$image" "$worker"
