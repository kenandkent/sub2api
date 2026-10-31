#!/usr/bin/env python3
"""Local loopback TCP relay for HTTP proxies. No traffic is interpreted or logged."""
import argparse
import asyncio
import fcntl
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import secrets
import shlex
import signal
import socket
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[2]
DEFAULT_CONFIG = ROOT / '.dev/opencode-proxy-relay.json'


def load_config(path):
    raw = path.read_bytes()
    data = json.loads(raw)
    if not isinstance(data, dict) or set(data) - {'routes', 'listen_host', 'upstream_interface'}:
        raise ValueError('config may only contain routes, listen_host and upstream_interface')
    if data.get('listen_host', '127.0.0.1') != '127.0.0.1':
        raise ValueError('listen_host must be 127.0.0.1')
    interface_index = None
    if 'upstream_interface' in data:
        interface = data['upstream_interface']
        if not isinstance(interface, str) or not interface or '\x00' in interface:
            raise ValueError('upstream_interface must be a nonempty interface name')
        if sys.platform != 'darwin':
            raise ValueError('upstream_interface is supported only on macOS')
        try:
            interface_index = socket.if_nametoindex(interface)
        except OSError:
            raise ValueError('upstream_interface does not exist') from None
    routes = data.get('routes')
    if not isinstance(routes, list) or not routes:
        raise ValueError('routes must be a nonempty list')
    seen = set()
    for route in routes:
        if not isinstance(route, dict) or set(route) != {'listen_port', 'upstream_host', 'upstream_port'}:
            raise ValueError('each route must contain only listen_port, upstream_host and upstream_port')
        for key in ('listen_port', 'upstream_port'):
            if type(route[key]) is not int or not 1 <= route[key] <= 65535:
                raise ValueError('ports must be integers between 1 and 65535')
        if route['listen_port'] in seen:
            raise ValueError('duplicate listen port')
        seen.add(route['listen_port'])
        try:
            if not isinstance(route['upstream_host'], str):
                raise ValueError
            ipaddress.IPv6Address(route['upstream_host'])
        except (ValueError, TypeError):
            raise ValueError('upstream_host must be an IPv6 address') from None
    if interface_index is not None:
        routes = [dict(route, _interface_index=interface_index) for route in routes]
    return routes, hashlib.sha256(raw).hexdigest()


def fingerprint(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def process_info(pid):
    try:
        started = subprocess.check_output(['ps', '-p', str(pid), '-o', 'lstart='], text=True, stderr=subprocess.DEVNULL).strip()
        command = subprocess.check_output(['ps', '-ww', '-p', str(pid), '-o', 'command='], text=True, stderr=subprocess.DEVNULL).strip()
        return started, shlex.split(command)
    except (OSError, subprocess.CalledProcessError, ValueError):
        return None, []


def owned(state, config):
    try:
        pid, token, started = state['pid'], state['token'], state['started']
        if type(pid) is not int or pid <= 1 or not isinstance(token, str) or len(token) != 32:
            return False
        actual_started, args = process_info(pid)
        return (actual_started == started and os.getpgid(pid) == pid and
                str(Path(__file__).resolve()) in args and 'serve' in args and
                '--token' in args and args[args.index('--token') + 1] == token and
                '--config' in args and args[args.index('--config') + 1] == str(config))
    except (KeyError, TypeError, IndexError, ProcessLookupError, PermissionError):
        return False


def read_json(path):
    try:
        value = json.loads(path.read_text())
        return value if isinstance(value, dict) else {}
    except (OSError, ValueError):
        return {}


def atomic_json(path, value):
    temp = path.with_name(path.name + '.tmp.' + str(os.getpid()))
    temp.write_text(json.dumps(value))
    os.replace(temp, path)


def ready(state, marker, config):
    data = read_json(marker)
    try:
        return owned(state, config) and data == {'token': state['token'], 'config': fingerprint(config)}
    except (OSError, KeyError):
        return False


async def pipe(reader, writer):
    while True:
        chunk = await reader.read(65536)
        if not chunk:
            if writer.can_write_eof():
                writer.write_eof()
            return
        writer.write(chunk)
        await writer.drain()


async def forward(reader, writer, route):
    upstream_writer = None
    upstream_socket = None
    tasks = []
    try:
        if '_interface_index' in route:
            upstream_socket = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
            upstream_socket.setblocking(False)
            # macOS SDK netinet6/in6.h defines IPV6_BOUND_IF as 125; Python may omit it.
            upstream_socket.setsockopt(socket.IPPROTO_IPV6, getattr(socket, 'IPV6_BOUND_IF', 125),
                                       route['_interface_index'])
            await asyncio.wait_for(asyncio.get_running_loop().sock_connect(
                upstream_socket, (route['upstream_host'], route['upstream_port'])), timeout=10)
            upstream_reader, upstream_writer = await asyncio.open_connection(sock=upstream_socket)
            upstream_socket = None  # The stream now owns the socket.
        else:
            upstream_reader, upstream_writer = await asyncio.wait_for(
                asyncio.open_connection(route['upstream_host'], route['upstream_port']), timeout=10)
        tasks = [asyncio.create_task(pipe(reader, upstream_writer)),
                 asyncio.create_task(pipe(upstream_reader, writer))]
        await asyncio.gather(*tasks)
    except (OSError, asyncio.TimeoutError, ConnectionError):
        pass
    finally:
        if upstream_socket is not None:
            upstream_socket.close()
        for task in tasks:
            task.cancel()
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)
        for stream in (writer, upstream_writer):
            if stream is not None:
                stream.close()
        for stream in (writer, upstream_writer):
            if stream is not None:
                try:
                    await asyncio.wait_for(stream.wait_closed(), 1)
                except (OSError, asyncio.TimeoutError):
                    pass


async def serve(config, token, marker):
    routes, config_hash = load_config(config)
    servers, clients = [], set()
    stop_event = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, stop_event.set)

    def accept(reader, writer, route):
        task = asyncio.create_task(forward(reader, writer, route))
        clients.add(task)
        task.add_done_callback(clients.discard)

    try:
        for route in routes:
            server = await asyncio.start_server(lambda r, w, route=route: accept(r, w, route),
                                                '127.0.0.1', route['listen_port'])
            servers.append(server)
        atomic_json(marker, {'token': token, 'config': config_hash})
        await stop_event.wait()
    finally:
        if read_json(marker).get('token') == token:
            marker.unlink(missing_ok=True)
        for server in servers:
            server.close()
        for task in list(clients):
            task.cancel()
        if clients:
            await asyncio.gather(*clients, return_exceptions=True)
        for server in servers:
            # Python 3.12+: wait_closed() blocks until every accepted connection
            # is gone, so it must run after the client tasks are cancelled.
            try:
                await asyncio.wait_for(server.wait_closed(), 2)
            except (OSError, asyncio.TimeoutError):
                pass


def stop_process(state, config):
    if not owned(state, config):
        return not pid_exists(state.get('pid'))
    pid = state['pid']
    try:
        os.killpg(pid, signal.SIGTERM)
    except ProcessLookupError:
        return not pid_exists(pid)
    deadline = time.monotonic() + 4
    while time.monotonic() < deadline:
        if not pid_exists(pid):
            return True
        if not owned(state, config):
            return not pid_exists(pid)
        time.sleep(0.05)
    if not owned(state, config):
        return not pid_exists(pid)
    try:
        os.killpg(pid, signal.SIGKILL)
    except ProcessLookupError:
        return not pid_exists(pid)
    deadline = time.monotonic() + 1
    while time.monotonic() < deadline:
        if not pid_exists(pid):
            return True
        if not owned(state, config):
            return not pid_exists(pid)
        time.sleep(0.05)
    return not pid_exists(pid)


def pid_exists(pid):
    if type(pid) is not int or pid <= 1:
        return False
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


def reap_child(process):
    # Popen owns this freshly spawned child, so its PID cannot be reused before wait.
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=2)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('up', 'stop', 'status', 'serve'))
    parser.add_argument('--config', type=Path, default=DEFAULT_CONFIG)
    parser.add_argument('--token', help=argparse.SUPPRESS)
    args = parser.parse_args()
    config = args.config.resolve()
    state_file = config.parent / 'proxy-relay.pid.json'
    marker = config.parent / 'proxy-relay.ready.json'
    if args.action == 'serve':
        if not args.token:
            parser.error('serve requires --token')
        asyncio.run(serve(config, args.token, marker))
        return 0
    config.parent.mkdir(parents=True, exist_ok=True)
    with (config.parent / 'proxy-relay.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        state = read_json(state_file)
        stored_config = state.get('config')
        if isinstance(stored_config, str) and stored_config != str(config):
            if args.action == 'status':
                print('relay not ready')
            else:
                print('another config owns this relay state directory', file=sys.stderr)
            return 1
        if args.action == 'status':
            if ready(state, marker, config):
                print('relay running')
                return 0
            print('relay not ready')
            return 1
        if state_file.exists() and not state:
            print('relay state is unreadable; refusing to alter it', file=sys.stderr)
            return 1
        if not owned(state, config) and pid_exists(state.get('pid')):
            print('relay process identity could not be verified', file=sys.stderr)
            return 1
        if args.action == 'stop':
            if owned(state, config):
                if not stop_process(state, config):
                    print('relay did not stop', file=sys.stderr)
                    return 1
                print('relay stopped')
            else:
                print('relay not running')
            state_file.unlink(missing_ok=True)
            marker.unlink(missing_ok=True)
            return 0
        load_config(config)
        if ready(state, marker, config):
            print('relay already running')
            return 0
        if owned(state, config):
            print('relay is running with a different or unready configuration; stop it first', file=sys.stderr)
            return 1
        marker.unlink(missing_ok=True)
        state_file.unlink(missing_ok=True)
        log_dir = config.parent / 'logs'
        log_dir.mkdir(exist_ok=True)
        token = secrets.token_hex(16)
        with (log_dir / 'proxy-relay.log').open('a') as log:
            process = subprocess.Popen([sys.executable, str(Path(__file__).resolve()), 'serve',
                                        '--config', str(config), '--token', token],
                                       stdin=subprocess.DEVNULL, stdout=log, stderr=log,
                                       start_new_session=True, close_fds=True)
        started, _ = process_info(process.pid)
        state = {'pid': process.pid, 'token': token, 'started': started, 'config': str(config)}
        if not started or not owned(state, config):
            reap_child(process)
            marker.unlink(missing_ok=True)
            print('relay failed to establish process identity', file=sys.stderr)
            return 1
        try:
            atomic_json(state_file, state)
        except OSError:
            reap_child(process)
            marker.unlink(missing_ok=True)
            raise
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if ready(state, marker, config):
                print('relay started')
                return 0
            if process.poll() is not None:
                break
            time.sleep(0.05)
        reap_child(process)
        state_file.unlink(missing_ok=True)
        marker.unlink(missing_ok=True)
        print('relay failed to start; see logs/proxy-relay.log', file=sys.stderr)
        return 1


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f'relay error: {exc}', file=sys.stderr)
        sys.exit(1)
