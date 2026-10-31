#!/usr/bin/env bash
# Production-only snapshot runner. Install in /home/workspace/subapi.
set -euo pipefail
umask 077
# Do not inherit xtrace: Docker diagnostics and archive contents can be private.
set +x
root=${1:-/home/workspace/subapi}
[[ $# -le 1 && $root =~ ^/[a-zA-Z0-9_./-]+$ && $root != / ]] || { echo 'Backup failed: invalid root.' >&2; exit 1; }
case "/$root/" in */../*|*/./*) echo 'Backup failed: invalid root.' >&2; exit 1;; esac
# Suppress dependency diagnostics; never echo .env, Compose configuration or rows.
exec 3>&2 2>/dev/null
stage= lock= published=
cleanup() {
    code=$?
    [[ -z $stage ]] || rm -rf -- "$stage"
    [[ -z $lock ]] || rmdir -- "$lock"
    if (( code != 0 )); then echo 'Production backup failed; no failed snapshot was published and retention was not run.' >&3; fi
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
[[ -d $root && ! -L $root && -s $root/.env && -s $root/current-deployment && -d $root/data ]]
[[ ! -L $root/.env && ! -L $root/current-deployment && ! -L $root/data ]]
# Share the deployment helper's lock: snapshots must not race app migrations/releases.
mkdir "$root/.deploy-lock"
lock="$root/.deploy-lock"
[[ $(docker context show) == default && -z ${DOCKER_HOST:-} && ${DOCKER_CONTEXT:-default} == default ]]
mapfile -t state < "$root/current-deployment"
[[ ${#state[@]} == 1 ]]
read -r -a fields <<< "${state[0]}"
[[ ${#fields[@]} == 2 || ${#fields[@]} == 3 ]]
release=${fields[0]} image=${fields[1]} worker=${fields[2]:--}
[[ $release =~ ^[0-9a-f]{40}-[0-9]+-[0-9]+$ ]]
[[ $image =~ ^ghcr\.io/kenandkent/sub2api@sha256:[0-9a-f]{64}$ ]]
[[ $worker == - || $worker =~ ^ghcr\.io/kenandkent/sub2api-opencode-free-worker@sha256:[0-9a-f]{64}$ ]]
[[ -d $root/releases/$release && ! -L $root/releases && ! -L $root/releases/$release ]]
for file in docker-compose.local.yml docker-compose.production.yml; do [[ -f $root/releases/$release/$file ]]; done
if [[ $worker != - ]]; then [[ -f $root/releases/$release/docker-compose.opencode-free.production.yml ]]; fi
base="$root/backups/production"
[[ ! -L $root/backups && ! -L $base ]]
mkdir -p "$base"
chmod 700 "$base"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
final="$base/production-$stamp"
[[ ! -e $final && ! -L $final ]]
stage=$(mktemp -d "$base/.incomplete.XXXXXX")
cp -- "$root/current-deployment" "$stage/current-deployment"
services=(sub2api postgres redis)
[[ $worker == - ]] || services+=(opencode-free-worker)
pg= redis=
for service in "${services[@]}"; do
    id=$(docker ps --quiet --filter label=com.docker.compose.project=mysubapi --filter "label=com.docker.compose.service=$service")
    [[ -n $id && $id != *$'\n'* ]]
    # Only safe identity fields. Full inspect exposes credentials.
    printf '%s ' "$service" >> "$stage/images.txt"
    docker inspect "$id" --format '{{.Id}} {{.Image}} {{.Config.Image}} {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}' >> "$stage/images.txt"
    docker image inspect "$(docker inspect "$id" --format '{{.Image}}')" --format '{{.Id}} {{json .RepoDigests}} {{with index .Config "Labels"}}{{index . "org.opencontainers.image.revision"}}{{end}}' >> "$stage/images.txt"
    case $service in
        postgres) pg=$id;;
        redis) redis=$id;;
        sub2api) [[ $(docker inspect "$id" --format '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{end}}{{end}}') == "$root/data" ]];;
    esac
done
# pg_dump uses a transaction-consistent snapshot. Credentials stay inside container.
docker exec "$pg" sh -c 'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$stage/database.dump"
[[ -s $stage/database.dump ]]
docker exec -i "$pg" pg_restore --list < "$stage/database.dump" >/dev/null
# Redis contains transient sessions and billing cache state: include an RDB snapshot.
# Existing REDISCLI_AUTH is inherited inside Redis; no credentials on the host argv.
docker exec "$redis" redis-cli --rdb - > "$stage/redis.rdb"
[[ $(head -c 5 "$stage/redis.rdb") == REDIS ]]
# Live PostgreSQL/Redis data directories are deliberately excluded. Filesystem
# files, PostgreSQL and Redis are individually consistent, not one transaction.
tar -czf "$stage/recovery.tar.gz" -C "$root" -- .env data current-deployment "releases/$release"
cmp -s "$root/current-deployment" "$stage/current-deployment"
printf 'format=production-backup-v1\ncreated_utc=%s\nrelease=%s\nretention_days=14\nconsistency=postgres-transaction;redis-rdb;files-live;cross-store-not-atomic\n' "$stamp" "$release" > "$stage/manifest.txt"
(cd "$stage" && sha256sum database.dump redis.rdb recovery.tar.gz current-deployment images.txt manifest.txt > SHA256SUMS && sha256sum --check --status SHA256SUMS)
printf 'production-backup-v1\n' > "$stage/SUCCESS"
chmod 600 "$stage"/*
# Flush snapshot content before publishing. sync -f is supplied by GNU coreutils.
sync -f "$stage"
mv -- "$stage" "$final"
stage=
published=$final
sync -f "$base"
printf 'Production backup published: production-%s\n' "$stamp"
# Retention is entered only after complete publication. Do not follow symlinks,
# inspect unrelated backups, infer age from mtime, or prune incomplete snapshots.
cutoff=$(date -u -d '14 days ago' +%s)
for old in "$base"/production-*; do
    [[ -d $old && ! -L $old && $old != "$published" ]] || continue
    name=${old##*/}
    [[ $name =~ ^production-([0-9]{8}T[0-9]{6}Z)$ ]] || continue
    [[ -f $old/SUCCESS && ! -L $old/SUCCESS && $(cat "$old/SUCCESS") == production-backup-v1 ]] || continue
    token=${BASH_REMATCH[1]:-}
    # BASH_REMATCH can be changed by later checks; derive the validated name.
    token=${name#production-}
    iso="${token:0:4}-${token:4:2}-${token:6:2} ${token:9:2}:${token:11:2}:${token:13:2} UTC"
    epoch=$(date -u -d "$iso" +%s) || continue
    [[ $(date -u -d "@$epoch" +%Y%m%dT%H%M%SZ) == "$token" ]] || continue
    if (( epoch < cutoff )); then rm -rf -- "$old"; fi
done
