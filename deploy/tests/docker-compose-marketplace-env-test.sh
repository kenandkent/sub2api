#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

for flag in ENABLED ONBOARDING_ENABLED DISPATCH_ENABLED PAYOUTS_ENABLED; do
  grep -Fqx "MARKETPLACE_${flag}=false" deploy/.env.example || {
    printf 'Marketplace switch %s must default to false\n' "$flag" >&2
    exit 1
  }
done

for compose_file in deploy/docker-compose.yml deploy/docker-compose.local.yml \
  deploy/docker-compose.standalone.yml deploy/docker-compose.dev.yml; do
  awk '/^MARKETPLACE_[A-Z0-9_]+=/ { print }' deploy/.env.example |
    while IFS='=' read -r key value; do
      expected=$(printf '      - %s=${%s:-%s}' "$key" "$key" "$value")
      count=$(grep -Fxc -- "$expected" "$compose_file" || true)
      if [ "$count" -ne 1 ]; then
        printf '%s must forward %s exactly once with its documented default\n' "$compose_file" "$key" >&2
        exit 1
      fi
    done
done

printf 'docker compose Marketplace environment test passed\n'
