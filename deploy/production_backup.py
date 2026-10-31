#!/usr/bin/env python3
"""Private production snapshot transfer, verification and isolated restore checks."""
import argparse
import hashlib
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
DESTINATION = ROOT / '.cache/production-backups'
REMOTE_ROOT = '/home/workspace/subapi'
REMOTE_BASE = REMOTE_ROOT + '/backups/production'
HOST = 'root@167.114.210.84'
NAME = re.compile(r'production-[0-9]{8}T[0-9]{6}Z\Z')
PAYLOAD = {'database.dump', 'redis.rdb', 'recovery.tar.gz', 'current-deployment', 'images.txt', 'manifest.txt'}
FILES = PAYLOAD | {'SHA256SUMS', 'SUCCESS'}
MARKER = '# sub2api-production-backup-v1'
JOB = '30 19 * * * PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin /bin/bash /home/workspace/subapi/backup-production.sh >> /home/workspace/subapi/backups/production/schedule.log 2>&1 ' + MARKER


def verify(folder):
    """Verify a complete, flat snapshot without reading secrets into diagnostics."""
    folder = Path(folder)
    if folder.is_symlink() or not folder.is_dir():
        raise ValueError('snapshot must be a real directory')
    if {path.name for path in folder.iterdir()} != FILES:
        raise ValueError('snapshot file set is incomplete or unexpected')
    for path in folder.iterdir():
        if path.is_symlink() or not path.is_file():
            raise ValueError('snapshot contains a nonregular file')
    if (folder / 'SUCCESS').read_bytes() != b'production-backup-v1\n':
        raise ValueError('snapshot has no valid success marker')
    checksums = {}
    for line in (folder / 'SHA256SUMS').read_text().splitlines():
        match = re.fullmatch(r'([0-9a-f]{64})  ([a-zA-Z0-9._-]+)', line)
        if not match or match[2] not in PAYLOAD or match[2] in checksums:
            raise ValueError('invalid checksum manifest')
        checksums[match[2]] = match[1]
    if checksums.keys() != PAYLOAD:
        raise ValueError('checksum manifest is incomplete')
    for name, digest in checksums.items():
        value = hashlib.sha256()
        with (folder / name).open('rb') as file:
            for chunk in iter(lambda: file.read(1024 * 1024), b''):
                value.update(chunk)
        if value.hexdigest() != digest:
            raise ValueError('snapshot checksum mismatch')
    with (folder / 'database.dump').open('rb') as file:
        if file.read(5) != b'PGDMP':
            raise ValueError('snapshot is not a PostgreSQL custom archive')
    with (folder / 'redis.rdb').open('rb') as file:
        if file.read(5) != b'REDIS':
            raise ValueError('snapshot has no Redis RDB header')
    return folder


def ssh(command, **kwargs):
    # No host-key learning or writes to ~/.ssh. Use the repository's pinned key.
    args = ['ssh', '-F', '/dev/null', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
            '-o', 'UserKnownHostsFile=' + str(ROOT / 'deploy/ssh/mysubapi_known_hosts'),
            '-o', 'GlobalKnownHostsFile=/dev/null', '-o', 'ConnectTimeout=15',
            '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=3', HOST, command]
    return subprocess.run(args, stderr=subprocess.DEVNULL, check=True, **kwargs)


def private_destination():
    # Do not follow an operator-created symlink out of this project's boundary.
    for folder in (ROOT / '.cache', DESTINATION):
        if folder.is_symlink():
            raise ValueError('offsite destination must not be a symlink')
        folder.mkdir(mode=0o700, exist_ok=True)
        folder.chmod(0o700)
    return DESTINATION


def pull(name=None):
    destination = private_destination()
    if name is None:
        listing = ssh("find " + REMOTE_BASE + " -mindepth 2 -maxdepth 2 -type f -name SUCCESS -print", stdout=subprocess.PIPE).stdout.decode('ascii')
        names = [Path(line).parent.name for line in listing.splitlines() if NAME.fullmatch(Path(line).parent.name)]
        if not names:
            raise ValueError('no published production snapshots are available')
        name = max(names)
    if not NAME.fullmatch(name):
        raise ValueError('invalid snapshot name')
    final = destination / name
    if final.exists() or final.is_symlink():
        return verify(final)
    stage = Path(tempfile.mkdtemp(prefix='.incomplete.', dir=destination))
    try:
        # Save the outer transport archive privately, then extract only exact flat
        # regular files. Never extract the recovery archive or arbitrary tar paths.
        transport = stage / '.transport.tar'
        with transport.open('wb') as output:
            ssh(shlex.join(['tar', '-C', REMOTE_BASE + '/' + name, '-cf', '-', '--', *sorted(FILES)]), stdout=output)
        with tarfile.open(transport) as archive:
            members = archive.getmembers()
            if len(members) != len(FILES) or {m.name for m in members} != FILES or any(not m.isfile() for m in members):
                raise ValueError('unexpected transfer archive members')
            for member in members:
                with archive.extractfile(member) as source, (stage / member.name).open('xb') as output:
                    shutil.copyfileobj(source, output)
                (stage / member.name).chmod(0o600)
        transport.unlink()
        verify(stage)
        # No replacement of a previous successful offsite snapshot.
        if final.exists() or final.is_symlink():
            raise ValueError('snapshot destination appeared during transfer')
        stage.rename(final)
        return final
    finally:
        if stage.exists():
            shutil.rmtree(stage)


def update_crontab(previous):
    if any(re.match(r'^\s*(?:CRON_TZ|TZ)\s*=', line) for line in previous.splitlines()):
        raise ValueError('existing crontab timezone directives require manual review')
    lines = previous.splitlines(keepends=True)
    # Only remove complete job lines owned by this exact marker.
    result = ''.join(line for line in lines if not line.rstrip('\r\n').endswith(' ' + MARKER))
    if result and not result.endswith('\n'):
        result += '\n'
    return result + JOB + '\n'


def install_schedule():
    zone = ssh('readlink -f /etc/localtime', stdout=subprocess.PIPE).stdout.decode().strip()
    if zone not in ('/usr/share/zoneinfo/UTC', '/usr/share/zoneinfo/Etc/UTC'):
        raise ValueError('automatic schedule installation requires a UTC host timezone')
    # Distinguish no crontab (exit 1) from read/permission failures. Capture private
    # crontab contents without emitting them; modify only the owned job line.
    previous = ssh("crontab -l 2>/dev/null || { status=$?; if [ \"$status\" = 1 ]; then exit 0; else exit \"$status\"; fi; }", stdout=subprocess.PIPE).stdout.decode()
    updated = update_crontab(previous)
    payload = (ROOT / 'deploy/backup-production.sh').read_bytes()
    # Runner/log writes stay in the approved deployment root. Root crontab is
    # the explicit exception authorized by the user's daily schedule request.
    ssh('umask 077; test ! -L ' + REMOTE_ROOT + '/backups && test ! -L ' + REMOTE_BASE +
        ' && mkdir -p ' + REMOTE_BASE + ' && chmod 700 ' + REMOTE_BASE +
        ' && test ! -L ' + REMOTE_BASE + '/schedule.log && touch ' + REMOTE_BASE + '/schedule.log && chmod 600 ' + REMOTE_BASE + '/schedule.log')
    ssh('umask 077; tmp=$(mktemp ' + REMOTE_ROOT + '/.backup-runner.XXXXXX) && trap \'rm -f "$tmp"\' EXIT && cat > "$tmp" && chmod 700 "$tmp" && mv -- "$tmp" ' + REMOTE_ROOT + '/backup-production.sh', input=payload)
    # Re-read just before replacement; do not overwrite a concurrently changed job.
    current = ssh("crontab -l 2>/dev/null || { status=$?; if [ \"$status\" = 1 ]; then exit 0; else exit \"$status\"; fi; }", stdout=subprocess.PIPE).stdout.decode()
    if current != previous:
        raise ValueError('crontab changed during installation; retry')
    if updated != previous:
        ssh('crontab -', input=updated.encode())
    installed = ssh('crontab -l', stdout=subprocess.PIPE).stdout.decode()
    if installed != updated:
        raise ValueError('installed schedule verification failed')


def restore_check(folder, image):
    folder = verify(folder)
    if not re.fullmatch(r'(?:docker\.io/library/)?postgres@sha256:[0-9a-f]{64}', image):
        raise ValueError('restore requires an explicit immutable PostgreSQL image digest')
    if os.environ.get('DOCKER_HOST'):
        raise ValueError('restore requires a named local Docker context without DOCKER_HOST override')
    context = subprocess.run(['docker', 'context', 'show'], capture_output=True, check=True, text=True).stdout.strip()
    if not context:
        raise ValueError('restore requires an active Docker context')
    docker = ['docker', '--context', context]
    endpoint = subprocess.run(docker + ['context', 'inspect', context, '--format', '{{.Endpoints.docker.Host}}'], capture_output=True, check=True, text=True).stdout.strip()
    if not endpoint.startswith('unix://'):
        raise ValueError('restore requires a local Unix Docker socket')
    name = 'sub2api-backup-check-' + uuid.uuid4().hex[:16]
    created = False
    def run(*args, **kwargs):
        return subprocess.run(docker + list(args), check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, **kwargs)
    try:
        # No host port, no network, no production bind mount, no anonymous volume.
        # The image may initialize as postgres: allow PGDATA creation in the
        # sticky tmpfs parent; initdb makes the database directory private 0700.
        run('run', '--detach', '--name', name, '--label', 'sub2api.backup-restore-check=true', '--network', 'none',
            '--tmpfs', '/var/lib/postgresql:rw,mode=1777', '-e', 'POSTGRES_HOST_AUTH_METHOD=trust',
            '-e', 'POSTGRES_DB=restore_check', '-e', 'PGDATA=/var/lib/postgresql/data', image)
        created = True
        for attempt in range(60):
            try:
                # The official entrypoint's temporary initialization server
                # listens only on Unix sockets. TCP plus SELECT 1 proves the
                # final server and the actual target database are both ready.
                run('exec', name, 'psql', '-h', '127.0.0.1', '-U', 'postgres', '-d', 'restore_check',
                    '-v', 'ON_ERROR_STOP=1', '-Atqc', 'SELECT 1', timeout=5)
                break
            except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
                if attempt == 59:
                    raise ValueError('isolated PostgreSQL did not become ready')
                time.sleep(1)
        with (folder / 'database.dump').open('rb') as dump:
            run('exec', '-i', name, 'pg_restore', '--exit-on-error', '--no-owner', '--no-acl', '-U', 'postgres', '-d', 'restore_check', stdin=dump)
        # Require restored application schema/history without printing any rows.
        run('exec', name, 'psql', '-U', 'postgres', '-d', 'restore_check', '-v', 'ON_ERROR_STOP=1', '-Atqc',
            "DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM public.schema_migrations) THEN "
            "RAISE EXCEPTION 'restored migration history is empty'; END IF; END $$;")
    finally:
        if created:
            run('rm', '--force', '--volumes', name)


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    command = commands.add_parser('pull', help='pull the latest or a named production snapshot')
    command.add_argument('name', nargs='?')
    command = commands.add_parser('verify', help='check private snapshot checksums without extracting recovery data')
    command.add_argument('directory', type=Path)
    command = commands.add_parser('restore-check', help='restore only in an isolated, disposable local PostgreSQL container')
    command.add_argument('directory', type=Path)
    command.add_argument('--postgres-image', required=True)
    commands.add_parser('install-schedule', help='install production runner and daily 03:30 Asia/Shanghai cron on the UTC server')
    args = parser.parse_args()
    try:
        if args.command == 'pull':
            result = pull(args.name)
            print('Verified offsite snapshot: ' + str(result))
        elif args.command == 'verify':
            verify(args.directory)
            print('Snapshot checksums verified.')
        elif args.command == 'restore-check':
            restore_check(args.directory, args.postgres_image)
            print('Isolated PostgreSQL restore passed; disposable container removed.')
        elif args.command == 'install-schedule':
            install_schedule()
            print('Production schedule verified: 03:30 Asia/Shanghai daily (19:30 UTC). Offsite pull is separate.')
    except (ValueError, OSError, subprocess.SubprocessError, tarfile.TarError):
        # Do not print exception arguments: they can contain commands or content.
        print('Production backup operation failed; private diagnostics were suppressed.', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
