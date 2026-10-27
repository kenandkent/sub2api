# OpenCode free transport: local opt-in

## Local verification on 2026-10-27

The `mysubapi` implementation was built and deployed locally with the complete
Compose stack: application, PostgreSQL, Redis and the real Bun worker. All four
services were healthy after verification. No online server or OmniRoute change
was made. The normal feature default remains off; this local opt-in uses the
private `.dev/opencode-free.env` file.

The fixed runtime is Bun 1.4.2. The immutable profile is
`opencode-big-pickle-text-balanced-20261027-v2`, SHA-256
`da80fdbfd8112be5433856044945bbf09f926d06c1a4dc59b70624ab8bd9a0a9`.
Only explicitly selected public Zen accounts use this path. In the account
editor select Zen and “免费模型（Bun v2）”, use adaptive or Chat Completions and
the official Console Chat base, then map the public model name to `big-pickle`.
The first version accepts text Chat messages with temperature omitted or zero;
caller tools, media, other protocols and unverified parameters are rejected.

The offline full-stack check passed streaming, nonstreaming, actual-shaped
usage, nonzero input/output/cache-read pricing, balance reconciliation and
unknown/no charge after cancellation or missing usage. It used dedicated new
test objects and the existing Compose administrator's prior acknowledgement.
The isolated native development administrator's notice was left unaccepted;
that notice still requires the administrator's own confirmation.

Two subsequent real requests through the production worker also passed:

| Request | Duration | Prompt tokens, including cache | Completion tokens, including reasoning | Cache read |
| --- | --- | --- | --- | --- |
| Nonstream | 3.607 s | 178 | 140 | 69 |
| Stream | 6.904 s | 181 | 87 | 69 |

Both returned the exact requested text, complete responses and nested
`usage.opencode.billing_source: free`. Stream time to first text was 6.654 s.
The two new usage logs matched the balance deduction of 0.744 under deliberately
nonzero test prices (input 0.001, output 0.002, cache read 0.0005 per token).
These are test prices, not recommended production prices; upstream reported
cost zero. Reasoning was included once in completion, and cached input was
charged in its own bucket. Sanitized local evidence is in
`.cache/opencode-free-implementation/e2e-report.json` and
`.cache/opencode-free-implementation/real-e2e-report.json`. The test upstream
account and API key are now inactive; audit records are retained.

Full backend unit tests, 372 frontend test files / 2913 tests, frontend type
checking, 19 Bun worker tests / 72 assertions and the isolated PostgreSQL free
ledger integration tests passed. Database deduction, log insertion and
settlement state are transactional and deduplicated; auxiliary caches, platform
quotas and notifications are not guaranteed exactly once after process crashes.
These local samples do not establish production capacity or availability from
another egress IP.

The free transport is disabled unless both the global option and an account's
explicit free marker are enabled. The normal Compose file still starts the
original three services. This worker handles only the fixed OpenCode Console
`big-pickle` text profile.

## Native worker and backend

At the repository root, initialize a private token once (the existing file is
never overwritten):

```sh
python3 -B deploy/opencode/local.py init
python3 -B deploy/opencode/local.py worker
```

The worker stays in the foreground at `127.0.0.1:19090`, using Bun 1.4.2. The
launcher defaults to `~/.bun/bin/bun`; use `--bun /absolute/path/to/bun` to
select another executable. Start the existing isolated development PostgreSQL
and Redis, build `.cache/dev/bin/server`, then in another terminal run:

```sh
python3 -B deploy/opencode/local.py backend
```

The backend launcher uses the same development settings as `deploy/dev.py`,
plus `.dev/opencode-free.env`. It does not build, start dependencies, or manage
other processes. Its usual address is `127.0.0.1:18080`.

When an HTTP proxy is configured, any non-200 response through the proxy is
conservatively reported as a 502 worker error. This includes genuine upstream
429 responses, whose `Retry-After` may therefore be lost. The worker does not
automatically retry. HTTPS proxies and SOCKS proxies are rejected before dispatch.

The private file is created with mode 600 and contains the shared token; keep it private and do not
print it. The launcher rejects an existing file with wider permissions. Restart
both processes after rotating the file.

## Optional full Compose deployment

Initialize the private file as above, and use the overlay from the repository
root alongside the original deployment file:

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.yml -f deploy/docker-compose.opencode-free.yml config --services
docker compose --env-file deploy/.env -f deploy/docker-compose.yml -f deploy/docker-compose.opencode-free.yml up -d --build
```

The worker shares the `sub2api` network namespace, binds only its loopback,
and publishes no port. Both processes read the same private env file. Do not
use `docker compose config` without `--services` in shared logs, because it
renders resolved environment values. Compose's existing application and
persistence settings are otherwise unchanged.

## Offline end-to-end check

Use an isolated native development database. Start the backend as above; stop
any real worker and run the following test-only worker in its place:

```sh
python3 -B deploy/opencode/local.py mock-worker
```

In another terminal run `python3 -B deploy/opencode/e2e/run.py`. It creates
uniquely named test objects through the admin API and does not truncate or
remove existing data. The mock uses the real worker factory with an injected
fetch that checks the fixed upstream URL and returns deterministic SSE; no
model request is made. A sanitized report is written to
`.cache/opencode-free-implementation/e2e-report.json`. Restore the real worker
after testing. Settlement deduplication is covered separately by the ledger's
repository integration test; this script does not replay a private lease.

For an existing local full Compose deployment with an administrator who has
already accepted the usage notice, the explicit `--compose` mode uses
`deploy/.env` and its local app and database. It never accepts or bypasses that
notice. The test-only overlay mounts the local OpenCode files read-only and
starts the mock inside the worker container; its preflight checks that mock in
the shared container network namespace before creating test objects. Start the
mock worker only after the app is ready:

```sh
docker compose -p deploy --env-file deploy/.env -f deploy/docker-compose.local.yml -f deploy/docker-compose.opencode-free.yml -f deploy/opencode/e2e/docker-compose.mock.yml up -d --no-deps --force-recreate opencode-free-worker
python3 -B deploy/opencode/e2e/run.py --compose
```

After testing, restore the actual worker before any real model request:

```sh
docker compose -p deploy --env-file deploy/.env -f deploy/docker-compose.local.yml -f deploy/docker-compose.opencode-free.yml up -d --no-deps --force-recreate opencode-free-worker
```

Both commands recreate only the worker. They do not clear database or cache
data. A successful test saves a mode-600 private fixture under the project
cache for follow-up validation; it contains credentials and must stay private.
