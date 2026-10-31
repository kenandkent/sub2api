# Production backup and recovery checks

This tooling backs up only `root@167.114.210.84:/home/workspace/subapi`.
The production runner creates one private snapshot daily at **03:30
Asia/Shanghai**, retains successful production snapshots for **14 days**, and
leaves deployment services running. Local offsite copies belong in this
checkout's ignored `.cache/production-backups/` directory.

The daily cron job writes production snapshots. Copying a snapshot to this Mac
is a separate `pull` operation; a sleeping or disconnected Mac cannot receive an
automatic daily copy. Run `pull` regularly and after important changes. Production
retention never deletes local copies. The scripts do not delete or rotate older
pre-deployment dumps in `backups/` outside `backups/production/`.

## Snapshot contents and consistency

Each published `backups/production/production-YYYYMMDDTHHMMSSZ/` contains:

| File | Recovery purpose |
| --- | --- |
| `database.dump` | `pg_dump -Fc` transaction-consistent PostgreSQL database archive; validated with `pg_restore --list` before publication |
| `redis.rdb` | Redis replication-protocol RDB snapshot streamed with `redis-cli --rdb -`; existing container `REDISCLI_AUTH` supplies credentials |
| `recovery.tar.gz` | Private `.env`, application `data/`, `current-deployment`, and its referenced `releases/<id>/` files |
| `current-deployment` | Existing two/three-field deployment identity, preserved without changing its format |
| `images.txt` | Allowlisted running container/image IDs, configured image references, repository digests, OCI revisions, and health/status fields |
| `manifest.txt` | UTC timestamp, release ID, format and consistency policy |
| `SHA256SUMS` | SHA-256 digests for all six payload files |
| `SUCCESS` | Fixed success marker written after capture and checksum validation |

PostgreSQL's live filesystem is **not** copied. Redis's live AOF directory is
also excluded; the RDB is a logical recovery snapshot. Redis includes temporary
OAuth/passkey sessions, scheduling/rate-limit state, and billing fallback/cache
state, so this backup preserves it instead of assuming every key is disposable.

PostgreSQL, Redis and application files are captured sequentially. The database
archive is transaction consistent; Redis represents its RDB snapshot time. These
stores do **not** share an atomic transaction, and live application files can
change during capture. Tar detects changes it observes and fails the capture;
that does not provide a global filesystem snapshot. Restore requires reconciling
cross-store state; expired Redis sessions may disappear and cached counters may
reflect a different instant from PostgreSQL. Treat PostgreSQL as the durable
business-data source and review cache/session reuse before re-enabling traffic.
The tool acquires the existing `.deploy-lock` so deployment/migrations cannot race
the snapshot; it does not pause request handling or call model/payment services.

Release manifests and image identities identify the software needed for recovery;
the snapshot does not contain image layers. Keep access to the registry and
retain those immutable images. Image identity capture records observed runtime
state; use the deployment identity verifier separately to prove it matches the
expected release. The snapshot is not a release-health certification.

## Install and run

Requirements on the production Linux host: Bash 4+, Docker, GNU coreutils
(`date`, `sha256sum`, `sync -f`, `mktemp`), `tar` with gzip, and root's `crontab`.
PostgreSQL and Redis client utilities run inside their existing containers. The
runner locates exactly one running container for each expected service using
Compose project `mysubapi` labels and verifies the app's `/app/data` bind mount
points to the production `data/` directory. It uses the default local Docker
context and refuses a remote Docker context/host.

From the project root, install the runner and approved root cron entry:

```sh
python3 -B deploy/production_backup.py install-schedule
```

SSH uses the repository's pinned `deploy/ssh/mysubapi_known_hosts`, strict host
checking, batch authentication and no writes to `~/.ssh`. It deliberately ignores
SSH config; authentication uses default keys or the current SSH agent. No private
`.env` values, full Compose output, container environments or business rows are
printed. Dependency diagnostics are suppressed to avoid credentials in logs.

The installer verifies `/etc/localtime` resolves to UTC before adding `30 19 * *
*` (19:30 UTC is 03:30 the following day in Asia/Shanghai). It refuses existing
`TZ`/`CRON_TZ` directives instead of assuming a cron implementation supports
`CRON_TZ`. It replaces only lines ending with the exact
`# sub2api-production-backup-v1` marker, preserves other jobs, checks for concurrent
crontab edits, and verifies the resulting crontab. Repeated installation results
in one owned entry. It installs the script at
`/home/workspace/subapi/backup-production.sh` and private log at
`backups/production/schedule.log`. Host timezone must stay UTC.

For a first snapshot or manual retry, use the same strict SSH options as the
Python tool and execute this command on the production host:

```sh
/bin/bash /home/workspace/subapi/backup-production.sh
```

The script accepts an explicit deployment root for a controlled fixture/recovery
installation, but production cron always uses the fixed approved default root.
Never point it at a development deployment or use a remote Docker daemon.

## Offsite verification and isolated restore

Pull and verify the latest published snapshot, or select one by its exact name:

```sh
python3 -B deploy/production_backup.py pull
python3 -B deploy/production_backup.py pull production-20261031T193000Z
python3 -B deploy/production_backup.py verify .cache/production-backups/production-20261031T193000Z
```

A transfer is staged privately, checks every checksum and required file, and
renames the directory only after verification. Unexpected tar members, symlinks,
missing payloads and modified files fail verification. Existing local copies are
verified and reused, never overwritten. Temporary transfer files stay under the
project's `.cache/production-backups/` and are removed on failure. Local directories
are mode 700 and files mode 600. Never commit or share these private files.
Checksums detect corruption; SSH supplies transport authenticity. This is not
backup encryption at rest; protect the host/Mac and disk encryption separately.

Use a PostgreSQL image matching the production major version (currently 18) and
supply its immutable digest from `images.txt`:

```sh
python3 -B deploy/production_backup.py restore-check \
  .cache/production-backups/production-20261031T193000Z \
  --postgres-image 'postgres@sha256:REPLACE_WITH_VERIFIED_DIGEST'
```

This verifies the snapshot, starts a unique disposable PostgreSQL container on the
active local Unix Docker socket (including Docker Desktop `desktop-linux`) with **no network or published ports**, stores
its data in tmpfs (a sticky writable parent allows unprivileged initialization;
`initdb` makes `PGDATA` mode 700), and restores into `restore_check` using `pg_restore
--exit-on-error --no-owner --no-acl`. Readiness requires a successful query over container-local TCP, which excludes
the official image's temporary Unix-socket initialization server. After restore,
it requires a nonempty `public.schema_migrations` history without printing
business rows and removes the container and its volumes, including on failure.
No application or worker starts and production is never contacted. Docker must
have the selected image available or be able to pull it. Available memory must
fit the restored database. This check proves archive restorability, not complete
application acceptance or a coordinated PostgreSQL/Redis restore.

A full disaster recovery is a separately reviewed operation: recreate the
recorded software versions and deployment files on a clean host, inspect the
archive privately before extraction, restore PostgreSQL, decide whether and how
to restore Redis into an empty instance (with matching compatible Redis version
and AOF handling), retain `.env` encryption/JWT keys and `data/`, then check
application behavior with outbound model/payment calls disabled. Never extract a
recovery archive over a live production deployment as an ad hoc restore check.

## Failure and retention behavior

An incomplete capture never becomes a published snapshot and never triggers
retention. The runner removes only its own current `.incomplete.*` staging
directory. A second job or deployment fails to acquire `.deploy-lock`; inspect a
stale lock after a crash instead of deleting it blindly. Unexpected power loss
can leave an incomplete directory or stale lock for manual inspection.

After successful publication and filesystem sync, retention considers only
immediate real directories named exactly `production-YYYYMMDDTHHMMSSZ` with the
owned success marker. It parses their UTC name timestamps, deletes those older
than 14 days, skips future dates, unrelated names, symlinks, incomplete snapshots
and the newly published snapshot. It does not use mtimes. Existing successful
copies survive capture, validation and transfer failures. Because the production
root filesystem is already near capacity, check available space and actual
snapshot growth regularly; these scripts perform no broad disk cleanup.

Run the focused local regression suite (all temporary files stay in the project):

```sh
python3 -B -m unittest deploy.tests.test_production_backup -v
```
