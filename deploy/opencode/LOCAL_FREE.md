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
original three services. As of this record the worker handled only the fixed
OpenCode Console `big-pickle` text profile; see the 2026-10-30 update below.

## Dynamic adaptation and agent mode: 2026-10-30

The backend and worker no longer fix a single `big-pickle` text profile. The
Go side admits the pinned profiles plus any model matching the `-free` suffix
rule (System One `jev-`/`gemini-` ids stay rejected), selects text or agent
mode from the request body, and derives each request's profile by mirroring
`gen_profiles.py` byte substitution over embedded `big-pickle` text and agent
templates. The worker loads the whole `deploy/opencode/profiles/` directory,
verifies every file against the source-pinned
`worker/manifest.generated.ts`, keeps pinned profiles authoritative, and
derives profiles for admissible models it does not pin; the envelope identity
must match `body.model`. Agent mode passes caller tools, `tool_choice` and
tool-result history through, and the SSE parser validates real `tool_calls`
streams. Bodies outside the audited allowlist are still rejected locally.

Offline checks after this change: the full backend unit suite
(`make test-unit`) and 24 Bun worker tests / 128 assertions, with Go-side
golden profile hashes matching `deploy/opencode/profiles/` byte for byte. No
real upstream free-model request was made in that offline round; the real
upstream verification happened later the same day and is recorded below.
Per-model production acceptance still has to run as described in
[OpenCode 接入兼容性核查](COMPATIBILITY.md).

## Real free-model verification: 2026-10-30 (later round)

Modification logic fixed by this round's real-call testing (each with a
regression test):

- `deploy/opencode/worker/chatBody.ts`: `validToolParameters` now checks the
  Array branch before the prototype-key guard, mirroring Go's
  `openCodeFreeWalkToolValue`. Any JSON array inside tool `parameters`
  (for example `required: ["city"]`) was previously rejected as
  `unsupported_request`, so every agent request surfaced as gateway 502.
  `worker.test.ts` agent-dispatch fixtures now carry `required` / `enum` /
  `anyOf` / `additionalProperties` arrays.
- `backend/internal/service/opencode_free_bun_parser.go`: `finalEvents`
  terminal frames (tool calls, `finish_reason`, usage) are now emitted with
  the `data: ` SSE prefix. They were bare JSON lines, which SSE consumers
  silently drop, so clients saw only content and `[DONE]` while the server
  settled normally. Parser and gateway tests now assert every client line
  starts with `data: `.
- `backend/internal/service/opencode_free_bun_gateway.go`: the free path
  calls `MarkResponseCommitted` after writing a terminal error response
  (upstream-status passthrough and the already-written `freeReject`
  branch). Previously the chat handler appended its generic
  `event: error` fallback after the already-complete body, corrupting
  upstream error responses into JSON+SSE mixtures.
- `deploy/dev.py`: `development_env` merges the strict private
  `.dev/opencode-free.env` when present, so `make dev` and `local.py`
  backends share the worker endpoint and token. A missing file keeps the
  transport disabled.

Verification method:

```sh
make dev                                   # backend 18080 + frontend 3000
python3 -B deploy/opencode/local.py worker # native worker on 19090
python3 -B deploy/opencode/e2e/probe_real_models.py --env dev \
  --models big-pickle,glm-5.3-flash-free,mimo-v2.6-flash-free,deepseek-v4.1-flash-free,nemotron-3.5-lightning-free
```

The probe creates isolated group/account/channel/user/key objects, exercises
non-stream, stream and agent requests per model, checks local admission
guards and reconciles usage rows against the balance delta. The sanitized
report is `.cache/opencode-free-implementation/real-models-report.json`.

Verification results (2026-10-30):

- `REAL MODEL PROBE PASSED`, 23/23 checks. Five models returned
  content + `finish_reason` + usage non-stream and content + terminal
  frames + `[DONE]` on streams. Usage rows = charged = settled = 10, and
  the balance delta equals the cost sum exactly.
- Local guards: `gemini-` alias rejected locally with 400; an unknown
  `-free` model reaches upstream (not locally rejected) and returns the
  upstream `Model is unavailable.` message; unknown aliases return 404.
- Agent requests reached the upstream with tools intact, but opencode
  returned HTTP 429 `FreeUsageLimitError` (`Retry-After` ≈ 17 h, free
  agent quota reset pending). The gateway forwards that error cleanly, so
  agent transport is verified; per-model agent acceptance must be re-run
  with this probe after the quota resets.
- `nemotron-3-ultra-free` returns upstream HTTP 400
  `Model is unavailable.` (same message as unknown models); treat it as an
  upstream availability issue, not a local rejection.
- Upstream burst limiting was observed: short windows where every request
  returns the same 429, recovering within minutes. Re-run the probe when
  that happens instead of treating it as a regression.
- Full unit suite `make test-unit` (58 packages, 0 fail), `go build` /
  `go vet` clean, `gofmt` clean on touched files, and `bun test
  deploy/opencode/worker/worker.test.ts` (24 pass / 128 assertions) all
  ran green after these fixes.
- Per user instruction the final round samples 5 of the 9 official models;
  `beam-free`, `flint-40b-free` and `nemotron-3.5-super-vl-free` passed
  non-stream in the earlier compose round but were not part of this
  sample.

## Agent 429 root cause (tool-name gate) and placeholder read fix:
2026-10-30 (final round)

Every agent (tools) request in production returned 429
`FreeUsageLimitError` while text requests passed. Isolation tests in a
quota-healthy window proved the upstream free tier inspects the declared
tool NAME set: any set lacking the official client's placeholder `read`
gets 429 (`lookup`, `get_weather`, five-tool sets, `tool_choice: none` —
none exempt), while `[read]`, `[read,bash,edit,grep,glob]` and
`[read,get_weather]` returned 200, including a clean `get_weather` call.
Reproduced on 3/3 models (`big-pickle`, `glm-5.3-flash-free`,
`mimo-v2.6-flash-free`). OmniRoute's `opencodeFreeTierContract.ts`
documents the same rule and merges the placeholder for callers. The port
gap was `worker.ts` replacing caller tools wholesale instead of merging
the placeholder; the previous round's "agent quota pending" attribution
was wrong (text 200 in the same window rules out quota).

Modification logic (each with a regression test):

- `deploy/opencode/worker/worker.ts` merges the official placeholder
  `read` (`{filePath}` schema, "Do not call this tool…" description)
  into agent requests whose caller tools omit it; caller-declared `read`
  passes through untouched; text/empty-tool paths unchanged.
- `backend/internal/service/opencode_free_bun_request.go` adds
  `openCodeFreeRequest.InjectedTools` plus `openCodeFreeInjectedTools`,
  mirroring the worker rule exactly.
- `backend/internal/service/opencode_free_bun_parser.go` accepts
  injected names during accumulation (including split `rea`/`d` deltas),
  strips injected calls at finish and reindexes the remaining client
  calls; if every call was injected, degrades `finish_reason` from
  `tool_calls` to `stop`. `tool_choice=none`, forced/required semantics
  and the no-`InjectedTools` path behave exactly as before.
- `backend/internal/service/opencode_free_bun_gateway.go` passes
  `reqNorm.InjectedTools` into the SSE options.
- `deploy/opencode/e2e/probe_real_models.py` fixes the agent billing
  count: the agent request itself (HTTP 200 = one usage row) and the
  continuation each count toward `charged`; previously only the
  continuation did, so row counts could never match once agents
  succeeded.

Verification method: `bun test deploy/opencode/` (65 pass / 175
assertions), `cd backend && make test-unit` (58 packages, 0 fail),
`go build ./... && go vet -tags=unit ./...`, gofmt clean on touched
files; `make dev` + native worker restart, then a strict 9/9 assertion
script and
`probe_real_models.py --env dev --models big-pickle,glm-5.3-flash-free,deepseek-v4.1-flash-free,mimo-v2.6-flash-free`.

Verification results (2026-10-30 evening):

- Strict script 9/9 HTTP 200: agent non-stream/stream with `lookup`,
  the previously-429 no-`read` set `[search_weather,get_info]`, a
  coax-to-call-injected-`read` scenario (200, `finish=stop`, no `read`
  leaked to the client — the model refuses per the placeholder
  description), text non-stream/stream regressions, glm/mimo cross-model
  agent, and tool-result continuation (200 + stop + content).
- Probe round 1: 4 models x (non-stream + stream + agent + continuation)
  = 16/16 HTTP 200; with the accounting fix the reconciliation check
  passes (usage rows = charged = settled).
- Probe round 2 hit genuine upstream free-quota exhaustion (text now 429
  "Free usage limit reached", `Retry-After` points at the next-day
  ~08:15 +0800 reset) — an environment event, not a regression; the fix
  verdict rests on round 1 and the strict script.
- Production acceptance with a real client requires deploying this fix
  and re-running inside a quota window.

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
other processes. Its usual address is `127.0.0.1:18080`. `make dev` merges
the same private file into the air-managed backend environment, so both entry
points share one worker token; keep the worker running on `127.0.0.1:19090`
while iterating, and re-run `make dev` after editing Go sources (air reloads
them automatically).

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
