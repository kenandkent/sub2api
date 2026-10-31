"""Production snapshots: atomic publication, private transfer, bounded deletion."""
import hashlib
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from unittest.mock import patch
import tarfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'deploy/backup-production.sh'
TOOL = ROOT / 'deploy/production_backup.py'
RELEASE = 'a' * 40 + '-1-1'
FAKE = r'''#!/usr/bin/env python3
import os, sys
from pathlib import Path
args = sys.argv[1:]
if args == ['context', 'show']:
    print('default')
elif args[0] == 'ps':
    print('cid-' + args[-1].split('=')[-1])
elif args[0] == 'inspect':
    if 'Mounts' in args[-1]: print(os.environ['FIXTURE'] + '/data')
    elif args[-1] == '{{.Image}}':
        print('sha256:' + ('2' if args[1] == 'cid-postgres' else '3' if args[1] == 'cid-redis' else '1') * 64)
    else: print('cid sha256:' + '1'*64 + ' image:tag running healthy')
elif args[:2] == ['image', 'inspect']:
    assert args[2].startswith('sha256:'), 'image metadata must use the immutable runtime image ID'
    unlabeled = os.environ.get('SCENARIO') == 'unlabeled-dependencies' and args[2] in ('sha256:' + '2'*64, 'sha256:' + '3'*64)
    if unlabeled and '.Config.Labels' in args[-1]:
        print('template parsing error: map has no entry for key Labels', file=sys.stderr)
        sys.exit(1)
    print(args[2] + ' [image@sha256:' + '2'*64 + ']' + ('' if unlabeled else ' revision'))
elif args[0] == 'exec':
    cmd = ' '.join(args)
    if 'pg_dump' in cmd:
        print('private-database-sentinel', file=sys.stderr)
        if os.environ.get('SCENARIO') == 'dump-fail': sys.exit(1)
        sys.stdout.buffer.write(b'PGDMPfixture')
    elif 'pg_restore' in cmd:
        assert sys.stdin.buffer.read().startswith(b'PGDMP')
        if os.environ.get('SCENARIO') == 'invalid-dump': sys.exit(1)
    elif 'redis-cli' in cmd:
        if os.environ.get('SCENARIO') == 'redis-fail': sys.exit(1)
        sys.stdout.buffer.write(b'REDIS0012fixture')
    else: sys.exit(3)
else: sys.exit(4)
'''


class SnapshotTests(unittest.TestCase):
    def setUp(self):
        (ROOT / '.cache/backup-tests').mkdir(parents=True, exist_ok=True)
        self.tmp = tempfile.TemporaryDirectory(dir=ROOT / '.cache/backup-tests')
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name)
        self.source = self.path / 'production'
        self.source.mkdir()
        (self.source / 'data').mkdir()
        (self.source / 'data/config.yaml').write_text('private-data-sentinel')
        (self.source / '.env').write_text('PASSWORD=private-config-sentinel\n')
        (self.source / 'current-deployment').write_text(RELEASE + ' ghcr.io/kenandkent/sub2api@sha256:' + '1'*64 + ' -\n')
        release = self.source / 'releases' / RELEASE
        release.mkdir(parents=True)
        for name in ('docker-compose.local.yml', 'docker-compose.production.yml'):
            (release / name).write_text('services: {}\n')
        self.bin = self.path / 'bin'
        self.bin.mkdir()
        (self.bin / 'docker').write_text(FAKE)
        (self.bin / 'docker').chmod(0o700)
        # The runner executes on GNU Linux; adapt only coreutils on this Mac.
        (self.bin / 'sync').write_text('#!/bin/sh\nexit 0\n')
        (self.bin / 'sync').chmod(0o700)
        (self.bin / 'date').write_text('''#!/usr/bin/env python3
import datetime, sys
args = sys.argv[1:]
now = datetime.datetime.now(datetime.timezone.utc)
if '-d' in args:
    value = args[args.index('-d') + 1]
    if value == '14 days ago': now -= datetime.timedelta(days=14)
    elif value.startswith('@'): now = datetime.datetime.fromtimestamp(int(value[1:]), datetime.timezone.utc)
    else: now = datetime.datetime.strptime(value, '%Y-%m-%d %H:%M:%S UTC').replace(tzinfo=datetime.timezone.utc)
print(str(int(now.timestamp())) if args[-1] == '+%s' else now.strftime(args[-1][1:]))
''')
        (self.bin / 'date').chmod(0o700)
        self.backups = self.source / 'backups/production'

    def run_backup(self, scenario='success'):
        return subprocess.run(['bash', str(SCRIPT), str(self.source)], env={**os.environ, 'PATH': str(self.bin) + ':' + os.environ['PATH'], 'FIXTURE': str(self.source), 'SCENARIO': scenario}, capture_output=True, text=True)

    def test_success_has_valid_checksums_and_private_modes(self):
        result = self.run_backup()
        self.assertEqual(result.returncode, 0, result.stderr)
        published = list(self.backups.glob('production-*'))
        self.assertEqual(len(published), 1)
        directory = published[0]
        for entry in [self.backups, directory, *directory.iterdir()]:
            self.assertEqual(entry.stat().st_mode & 0o777, 0o700 if entry.is_dir() else 0o600)
        for line in (directory / 'SHA256SUMS').read_text().splitlines():
            digest, name = line.split('  ')
            self.assertEqual(hashlib.sha256((directory / name).read_bytes()).hexdigest(), digest)
        self.assertIn('database.dump', (directory / 'SHA256SUMS').read_text())
        self.assertIn('redis.rdb', (directory / 'SHA256SUMS').read_text())
        self.assertNotIn('private-', result.stdout + result.stderr)
        with tarfile.open(directory / 'recovery.tar.gz') as archive:
            names = archive.getnames()
            self.assertIn('.env', names)
            self.assertIn('data/config.yaml', names)
            self.assertIn('releases/' + RELEASE + '/docker-compose.local.yml', names)
            self.assertFalse(any('postgres_data' in name or 'redis_data' in name for name in names))

    def test_unlabeled_official_dependency_images_still_publish_snapshot(self):
        result = self.run_backup('unlabeled-dependencies')
        self.assertEqual(result.returncode, 0, result.stderr)
        published = list(self.backups.glob('production-*'))
        self.assertEqual(len(published), 1)
        identities = (published[0] / 'images.txt').read_text()
        self.assertIn('sha256:' + '2'*64, identities)
        self.assertIn('sha256:' + '3'*64, identities)

    def test_failed_capture_does_not_publish_or_prune(self):
        for scenario in ('dump-fail', 'invalid-dump', 'redis-fail'):
            with self.subTest(scenario=scenario):
                old = self.backups / 'production-20000101T000000Z'
                old.mkdir(parents=True, exist_ok=True)
                (old / 'SUCCESS').write_text('keep')
                result = self.run_backup(scenario)
                self.assertNotEqual(result.returncode, 0)
                self.assertTrue(old.is_dir())
                self.assertEqual(list(self.backups.glob('production-*')), [old])
                self.assertNotIn('private-', result.stdout + result.stderr)

    def test_retention_only_deletes_successful_owned_older_names(self):
        for name in ('production-20000101T000000Z', 'production-29990101T000000Z', 'other-20000101T000000Z', 'production-20000102T000000Z'):
            folder = self.backups / name
            folder.mkdir(parents=True)
            if name != 'production-20000102T000000Z':
                (folder / 'SUCCESS').write_text('production-backup-v1\n')
        result = self.run_backup()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.backups / 'production-20000101T000000Z').exists())
        for name in ('production-29990101T000000Z', 'other-20000101T000000Z', 'production-20000102T000000Z'):
            self.assertTrue((self.backups / name).is_dir())

    def test_deployment_lock_blocks_capture(self):
        (self.source / '.deploy-lock').mkdir()
        self.assertNotEqual(self.run_backup().returncode, 0)
        self.assertFalse(self.backups.exists())


class LocalToolTests(unittest.TestCase):
    def setUp(self):
        (ROOT / '.cache/backup-tests').mkdir(parents=True, exist_ok=True)

    def load_tool(self):
        self.assertTrue(TOOL.exists(), 'local backup verifier is required')
        spec = importlib.util.spec_from_file_location('production_backup', TOOL)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def test_cron_preserves_jobs_and_is_idempotent(self):
        tool = self.load_tool()
        previous = 'MAILTO=ops@example.test\n10 2 * * * /usr/bin/other\n'
        result = tool.update_crontab(previous)
        self.assertTrue(result.startswith(previous))
        self.assertEqual(tool.update_crontab(result), result)
        self.assertIn('30 19 * * *', result)
        self.assertEqual(result.count('# sub2api-production-backup-v1'), 1)

    def test_cron_refuses_timezone_directives_that_change_schedule(self):
        tool = self.load_tool()
        with self.assertRaises(ValueError):
            tool.update_crontab('CRON_TZ=America/New_York\n')

    def make_snapshot(self, folder):
        payload = {'database.dump': b'PGDMPfixture', 'redis.rdb': b'REDIS0012fixture',
                   'recovery.tar.gz': b'opaque-private-archive', 'current-deployment': b'release image',
                   'images.txt': b'image identities', 'manifest.txt': b'format=production-backup-v1\n'}
        for name, value in payload.items():
            (folder / name).write_bytes(value)
        (folder / 'SUCCESS').write_text('production-backup-v1\n')
        (folder / 'SHA256SUMS').write_text(''.join(hashlib.sha256(value).hexdigest() + '  ' + name + '\n' for name, value in payload.items()))

    def test_verifier_rejects_path_escape_and_tampering(self):
        tool = self.load_tool()
        with tempfile.TemporaryDirectory(dir=ROOT / '.cache/backup-tests') as tmp:
            folder = Path(tmp)
            self.make_snapshot(folder)
            self.assertEqual(tool.verify(folder), folder)
            original = (folder / 'SHA256SUMS').read_text()
            (folder / 'SHA256SUMS').write_text(original.replace('  database.dump', '  ../outside'))
            with self.assertRaises(ValueError): tool.verify(folder)
            (folder / 'SHA256SUMS').write_text(original)
            (folder / 'database.dump').write_text('modified')
            with self.assertRaises(ValueError): tool.verify(folder)

    def test_restore_accepts_local_desktop_context_is_isolated_and_cleans_on_failure(self):
        tool = self.load_tool()
        commands = []
        def docker(args, **kwargs):
            commands.append(args)
            if args[-2:] == ['context', 'show']:
                return subprocess.CompletedProcess(args, 0, stdout='desktop-linux\n')
            if 'inspect' in args:
                return subprocess.CompletedProcess(args, 0, stdout='unix:///local/docker.sock\n')
            if 'pg_restore' in args:
                raise subprocess.CalledProcessError(1, args)
            return subprocess.CompletedProcess(args, 0)
        with tempfile.TemporaryDirectory(dir=ROOT / '.cache/backup-tests') as tmp:
            self.make_snapshot(Path(tmp))
            with patch.dict(os.environ, {'DOCKER_CONTEXT': 'desktop-linux'}, clear=True), patch.object(tool.subprocess, 'run', side_effect=docker):
                with self.assertRaises(subprocess.CalledProcessError):
                    tool.restore_check(Path(tmp), 'postgres@sha256:' + '1'*64)
        run = next(args for args in commands if 'run' in args)
        self.assertEqual(run[1:3], ['--context', 'desktop-linux'])
        self.assertIn('none', run)
        self.assertIn('--tmpfs', run)
        for flag in ('-p', '--publish', '-v', '--volume', '--mount', '--network=host'):
            self.assertNotIn(flag, run)
        self.assertTrue(any('rm' in args and '--volumes' in args for args in commands))

    def test_restore_waits_for_final_tcp_database_and_checks_migrations(self):
        tool = self.load_tool()
        state = {'tcp_attempts': 0, 'ready': False, 'migrations_checked': False}
        def docker(args, **kwargs):
            if args[-2:] == ['context', 'show']:
                return subprocess.CompletedProcess(args, 0, stdout='desktop-linux\n')
            if 'inspect' in args:
                return subprocess.CompletedProcess(args, 0, stdout='unix:///local/docker.sock\n')
            if 'psql' in args and args[-1] == 'SELECT 1':
                if '-h' not in args or args[args.index('-h') + 1] != '127.0.0.1':
                    raise subprocess.CalledProcessError(1, args)
                state['tcp_attempts'] += 1
                if state['tcp_attempts'] == 1:
                    raise subprocess.CalledProcessError(1, args)
                state['ready'] = True
            if 'pg_restore' in args and not state['ready']:
                raise subprocess.CalledProcessError(1, args)
            if 'psql' in args and 'schema_migrations' in args[-1]:
                state['migrations_checked'] = True
            return subprocess.CompletedProcess(args, 0)
        with tempfile.TemporaryDirectory(dir=ROOT / '.cache/backup-tests') as tmp:
            self.make_snapshot(Path(tmp))
            with patch.dict(os.environ, {}, clear=True), patch.object(tool.subprocess, 'run', side_effect=docker), patch.object(tool.time, 'sleep'):
                tool.restore_check(Path(tmp), 'postgres@sha256:' + '1'*64)
        self.assertEqual(state['tcp_attempts'], 2)
        self.assertTrue(state['migrations_checked'])

    def test_restore_tmpfs_allows_nonroot_image_initialization(self):
        tool = self.load_tool()
        commands = []
        def docker(args, **kwargs):
            commands.append(args)
            if args[-2:] == ['context', 'show']:
                return subprocess.CompletedProcess(args, 0, stdout='desktop-linux\n')
            if 'inspect' in args:
                return subprocess.CompletedProcess(args, 0, stdout='unix:///local/docker.sock\n')
            if 'run' in args:
                mount = args[args.index('--tmpfs') + 1]
                options = dict(option.split('=', 1) for option in mount.split(',') if '=' in option)
                # Tmpfs starts owned by root. A nonroot image must be able to
                # create its own PGDATA directory inside this empty parent.
                if int(options.get('mode', '1777'), 8) & 0o003 != 0o003:
                    raise subprocess.CalledProcessError(1, args)
            return subprocess.CompletedProcess(args, 0)
        with tempfile.TemporaryDirectory(dir=ROOT / '.cache/backup-tests') as tmp:
            self.make_snapshot(Path(tmp))
            with patch.dict(os.environ, {}, clear=True), patch.object(tool.subprocess, 'run', side_effect=docker):
                tool.restore_check(Path(tmp), 'postgres@sha256:' + '1'*64)
        self.assertTrue(any('pg_restore' in args for args in commands))
        self.assertTrue(any('rm' in args and '--volumes' in args for args in commands))

    def test_restore_rejects_remote_endpoint_without_starting_container(self):
        tool = self.load_tool()
        commands = []
        def docker(args, **kwargs):
            commands.append(args)
            value = 'remote-context\n' if args[-2:] == ['context', 'show'] else 'ssh://remote\n'
            return subprocess.CompletedProcess(args, 0, stdout=value)
        with tempfile.TemporaryDirectory(dir=ROOT / '.cache/backup-tests') as tmp:
            self.make_snapshot(Path(tmp))
            with patch.dict(os.environ, {}, clear=True), patch.object(tool.subprocess, 'run', side_effect=docker):
                with self.assertRaises(ValueError):
                    tool.restore_check(Path(tmp), 'postgres@sha256:' + '1'*64)
        self.assertFalse(any('run' in args for args in commands))

    def test_schedule_log_symlink_is_rejected_by_remote_installer(self):
        tool = self.load_tool()
        with tempfile.TemporaryDirectory(dir=ROOT / '.cache/backup-tests') as tmp:
            root = Path(tmp)
            base = root / 'backups/production'
            base.mkdir(parents=True)
            victim = root / 'unrelated'
            victim.write_text('preserve')
            victim.chmod(0o644)
            (base / 'schedule.log').symlink_to(victim)
            def remote(command, **kwargs):
                if command == 'readlink -f /etc/localtime':
                    return subprocess.CompletedProcess(command, 0, stdout=b'/usr/share/zoneinfo/Etc/UTC\n')
                if command.startswith('crontab -l'):
                    return subprocess.CompletedProcess(command, 0, stdout=b'')
                if command.startswith('umask 077; test ! -L '):
                    result = subprocess.run(command, shell=True, capture_output=True)
                    result.check_returncode()
                    return result
                raise AssertionError('unexpected remote command; test never runs a real crontab')
            with patch.object(tool, 'REMOTE_ROOT', str(root)), patch.object(tool, 'REMOTE_BASE', str(base)), patch.object(tool, 'ssh', side_effect=remote):
                with self.assertRaises(subprocess.CalledProcessError):
                    tool.install_schedule()
            self.assertEqual(victim.read_text(), 'preserve')
            self.assertEqual(victim.stat().st_mode & 0o777, 0o644)


if __name__ == '__main__':
    unittest.main()
