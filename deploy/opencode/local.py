#!/usr/bin/env python3
"""Opt-in OpenCode free worker and standalone native backend launcher."""
import argparse
import importlib.util
import os
from pathlib import Path
import re
import secrets
import stat
import sys

ROOT = Path(__file__).resolve().parents[2]


def settings(root):
    directory = root / '.dev'
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    path = directory / 'opencode-free.env'
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        pass
    else:
        with os.fdopen(fd, 'w') as f:
            f.write('OPENCODE_FREE_BUN_ENABLED=true\nOPENCODE_FREE_BUN_TOKEN=' + secrets.token_hex(32) + '\n')
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
        raise ValueError('Private environment file must be regular and mode 600')
    result = {}
    for line in path.read_text().splitlines():
        key, sep, value = line.partition('=')
        if not sep or key not in ('OPENCODE_FREE_BUN_ENABLED', 'OPENCODE_FREE_BUN_TOKEN') or key in result:
            raise ValueError('Invalid OpenCode environment file')
        result[key] = value
    if result.get('OPENCODE_FREE_BUN_ENABLED') != 'true' or not re.fullmatch(r'[0-9a-f]{64}', result.get('OPENCODE_FREE_BUN_TOKEN', '')):
        raise ValueError('Invalid OpenCode environment file')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('init', 'worker', 'mock-worker', 'backend'))
    parser.add_argument('--bun', default=os.environ.get('BUN_BIN', str(Path.home() / '.bun/bin/bun')))
    args = parser.parse_args()
    os.umask(0o077)
    try:
        config = settings(ROOT)
        if args.command == 'init':
            print('Private configuration ready at .dev/opencode-free.env')
            return
        if args.command in ('worker', 'mock-worker'):
            env = dict(os.environ, **config, LISTEN_HOST='127.0.0.1', PORT='19090',
                       PROFILES_DIR=str(ROOT / 'deploy/opencode/profiles'),
                       BUN_RUNTIME_TRANSPILER_CACHE_PATH='0')
            script = 'deploy/opencode/worker/index.ts' if args.command == 'worker' else 'deploy/opencode/e2e/mock-worker.ts'
            os.execve(args.bun, [args.bun, str(ROOT / script)], env)
        spec = importlib.util.spec_from_file_location('sub2api_dev', ROOT / 'deploy/dev.py')
        dev = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(dev)
        dev_settings = dev.initialize(ROOT)
        env = dev.development_env(ROOT, dev_settings)
        env.update(config)
        os.chdir(ROOT / 'backend')
        binary = str(ROOT / '.cache/dev/bin/server')
        os.execve(binary, [binary], env)
    except (OSError, ValueError) as error:
        print('OpenCode local error: ' + str(error), file=sys.stderr)
        sys.exit(1)


if __name__ == '__main__':
    main()
