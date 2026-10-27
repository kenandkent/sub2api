# mysubapi CI/CD

This branch uses `.github/workflows/mysubapi-ci-cd.yml`. This change prepares the pipeline; it does not publish an image, connect to the server, or deploy it during local development.

## What runs

Pushes to `mysubapi` and pull requests targeting it run deployment tests, backend unit/integration tests, and frontend lint/type/critical tests. Only trusted pushes or manual runs on `kenandkent/sub2api`'s `mysubapi` branch can publish images. Pull requests never publish or deploy.

After checks pass, the root Dockerfile builds `linux/amd64` and `linux/arm64` images with the exact commit, version and build date. GHCR receives `ghcr.io/kenandkent/sub2api:sha-<full SHA>` and the moving `:mysubapi` tag. Deployment always uses the build output's immutable `@sha256:...` digest, including its platform manifest.

GitHub only enables `workflow_dispatch` when the workflow file also exists on the repository's default branch. Until then, pushing `mysubapi` is the trigger; adding this file to this branch alone does not guarantee an Actions “Run workflow” button. Changing the default branch is not part of this setup.

## GitHub configuration

Create the `mysubapi-production` GitHub Environment and add these secrets:

| Secret | Value |
| --- | --- |
| `DEPLOY_SSH_KEY` | A private SSH key authorized for root on the target server and usable without an interactive passphrase prompt |
| `DEPLOY_KNOWN_HOSTS` | The verified OpenSSH known_hosts entry for `167.114.210.84` (use `[host]:port` for a nonstandard port) |

The local computer's SSH agent/configuration is not available on a hosted GitHub Runner. Configure these two secrets in GitHub; do not paste private keys into issues or commit them.

The workflow defaults to the server supplied for this project. The following repository or environment variables can override its non-secret connection settings:

| Variable | Default |
| --- | --- |
| `DEPLOY_HOST` | `167.114.210.84` |
| `DEPLOY_PORT` | `22` |
| `DEPLOY_USER` | `root` |
| `DEPLOY_PATH` | `/home/workspace/subapi` |

Use an absolute deployment path consisting of letters, digits, `_`, `.`, `/`, and `-`, without `.` or `..` path segments. This constraint allows SSH commands to pass the remote path safely.

**Deployment is disabled until the repository variable `MYSUBAPI_DEPLOY_ENABLED` is exactly `true`.** Set it at repository level, because the job condition is evaluated before environment-scoped variables are loaded. Keep it unset or `false` while preparing the server. Image publication still runs after a successful trusted branch push.

The build job uses its `GITHUB_TOKEN` with `packages: write`; the deployment job uses its own short-lived token with `packages: read` and forwards it through SSH stdin. No permanent GHCR password is stored on the server. If the GHCR package already exists, ensure it grants Actions access to `kenandkent/sub2api`; an unrelated existing package can otherwise return 403 even with workflow permissions.

## Prepare the server before enabling deployment

The server needs Docker Engine, Docker Compose **2.24.4 or later**, Bash, and standard Linux utilities including `timeout`. The root SSH account must be able to access Docker. The deployment directory is this application's project boundary; all uploaded releases, private temporary registry credentials, locks and backups stay inside it.

On the server, create `/home/workspace/subapi`, then place a private `.env` there using `deploy/.env.example` as a template. Configure at least `POSTGRES_PASSWORD`, `REDIS_PASSWORD`, `ADMIN_EMAIL`, `ADMIN_PASSWORD`, `JWT_SECRET`, and `TOTP_ENCRYPTION_KEY`; retain those values across releases. Set `.env` permissions to `600`. For a reverse proxy on the same host, set `BIND_HOST=127.0.0.1`; otherwise configure the desired bind address and host port explicitly.

The workflow does not upload the developer's `.env`, overwrite server secrets, or accept the first-login administrator acknowledgement. It fails early if the private server `.env` is absent. Configure the HTTPS reverse proxy separately when exposing the application to users.

New deployments use Compose project name `mysubapi` and the existing container names `sub2api`, `sub2api-postgres`, and `sub2api-redis`. If the server already runs this application under another Compose project, first identify its data directories and plan the handover; do not start a competing project with these container names or overwrite an existing database directory.

## Deployment and recovery

Each run uploads files into `incoming/<SHA>-<run ID>-<attempt>`, then publishes a unique immutable directory in `releases/`. Existing release directories are never overwritten, including reruns of the same commit. The helper takes a lock covering authentication, pulling, backup, application replacement and recovery. A lock left by a killed host/process must be investigated before removing it.

The production override is combined with `docker-compose.local.yml` and removes its `build` section. `--project-directory /home/workspace/subapi` fixes all bind-mounted data at `data/`, `postgres_data/`, and `redis_data/` under that directory, even though Compose files live inside a release subdirectory. The server does not need the source checkout or Go/Node toolchains.

The helper retries image downloads up to three times, limits each pull command to 180 seconds (then allows 10 seconds before a forced stop), reuses existing dependency images, waits for PostgreSQL/Redis and an SQL query, writes a private nonempty `pg_dump -Fc` backup, then replaces only the application. Container health, `/setup/status` and public site settings must all succeed before `current-deployment` is updated atomically. The health endpoint alone does not check database/Redis operation.

If application startup or a post-start probe fails, the helper tries the previous release files and image digest and still exits unsuccessfully. **This only attempts application recovery. Database migrations are not reversed, and an older application may be incompatible with an already migrated database.** Keep the backup, inspect migration compatibility, and perform any database restore deliberately. Failed pulls, SQL readiness checks or backups stop before application replacement.

`current-deployment` records `RELEASE_ID IMAGE_DIGEST`. Existing releases, dependency images and database backups are retained; no volume deletion or global Docker pruning is performed. Establish a backup/old-release retention policy after confirming available disk space.

For a manually selected, known-compatible release, the server helper can also be run without its optional registry user argument, using an existing Docker login/cache:

```bash
bash /home/workspace/subapi/releases/<release-id>/deploy-mysubapi.sh \
  /home/workspace/subapi <release-id> \
  ghcr.io/kenandkent/sub2api@sha256:<digest>
```

## Local verification

```bash
python3 -B -m unittest discover -s deploy/tests -p 'test_mysubapi_deploy.py'
sh deploy/tests/docker-compose-production-test.sh
```

The helper tests inject download, backup, startup and recovery failures without SSH, registry access or changes to the running local containers. The Compose checks use synthetic environment values and temporary directories inside the project. Local source deployment continues to use `docker compose -f deploy/docker-compose.local.yml up -d --build --wait`.
