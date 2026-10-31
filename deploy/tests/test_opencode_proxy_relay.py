import json
import os
import signal
import sys
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'deploy/opencode/proxy_relay.py'
TMP_ROOT = ROOT / '.dev/test-tmp'


def free_port(family=socket.AF_INET):
    with socket.socket(family) as sock:
        sock.bind(('127.0.0.1' if family == socket.AF_INET else '::1', 0))
        return sock.getsockname()[1]


class RelayTest(unittest.TestCase):
    def setUp(self):
        TMP_ROOT.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=TMP_ROOT)
        self.dir = Path(self.temp.name)
        self.config = self.dir / 'config.json'

    def tearDown(self):
        if self.config.exists():
            self.run_cli('stop')
        # A failing lifecycle test must not leave its own relay behind.
        lines = subprocess.check_output(['ps', '-ww', '-axo', 'pid,pgid,command'], text=True).splitlines()
        for line in lines:
            parts = line.strip().split(None, 2)
            if len(parts) == 3 and str(SCRIPT) in parts[2] and ' serve ' in parts[2] and str(self.config) in parts[2]:
                pid, pgid = int(parts[0]), int(parts[1])
                if pid == pgid:
                    try:
                        os.killpg(pid, signal.SIGTERM)
                    except ProcessLookupError:
                        pass
        self.temp.cleanup()

    def run_cli(self, action):
        return subprocess.run(['python3', '-B', str(SCRIPT), action, '--config', str(self.config)],
                              capture_output=True, text=True, timeout=15)

    def write_config(self, routes, **extra):
        self.config.write_text(json.dumps({'routes': routes, **extra}))

    def route(self, local, upstream):
        return {'listen_port': local, 'upstream_host': '::1', 'upstream_port': upstream}

    def test_forwards_bytes_both_ways_and_reuses_running_process(self):
        upstream = socket.socket(socket.AF_INET6)
        upstream.bind(('::1', 0))
        upstream.listen()
        local = free_port()
        self.write_config([self.route(local, upstream.getsockname()[1])])
        received = []

        def echo():
            conn, _ = upstream.accept()
            with conn:
                received.append(conn.recv(4096))
                conn.sendall(b'\x00reply\xff')
            upstream.close()

        thread = threading.Thread(target=echo, daemon=True)
        thread.start()
        first = self.run_cli('up')
        self.assertEqual(first.returncode, 0, first.stderr)
        state = json.loads((self.dir / 'proxy-relay.pid.json').read_text())
        second = self.run_cli('up')
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(json.loads((self.dir / 'proxy-relay.pid.json').read_text())['pid'], state['pid'])
        self.assertEqual(self.run_cli('status').returncode, 0)
        with socket.create_connection(('127.0.0.1', local), timeout=3) as conn:
            conn.sendall(b'CONNECT example:443\r\nProxy-Authorization: Basic secret\r\n\r\n\x00\xff')
            self.assertEqual(conn.recv(4096), b'\x00reply\xff')
        thread.join(3)
        self.assertEqual(received, [b'CONNECT example:443\r\nProxy-Authorization: Basic secret\r\n\r\n\x00\xff'])
        self.assertEqual(self.run_cli('stop').returncode, 0)
        self.assertNotEqual(self.run_cli('status').returncode, 0)
        self.assertNotIn('secret', ''.join(p.read_text() for p in (self.dir / 'logs').glob('*')))

    def test_stop_succeeds_while_client_connection_stays_open(self):
        upstream = socket.socket(socket.AF_INET6)
        upstream.bind(('::1', 0))
        upstream.listen()
        local = free_port()
        self.write_config([self.route(local, upstream.getsockname()[1])])

        def echo():
            conn, _ = upstream.accept()
            with conn:
                received = conn.recv(4096)
                conn.sendall(b'\x00reply\xff')
            upstream.close()

        thread = threading.Thread(target=echo, daemon=True)
        thread.start()
        self.assertEqual(self.run_cli('up').returncode, 0)
        conn = socket.create_connection(('127.0.0.1', local), timeout=3)
        try:
            conn.sendall(b'CONNECT example:443\r\n\r\n\x00\xff')
            self.assertEqual(conn.recv(4096), b'\x00reply\xff')
            thread.join(3)
            # The client stays connected: SIGTERM must still stop the relay.
            self.assertEqual(self.run_cli('stop').returncode, 0)
            self.assertNotEqual(self.run_cli('status').returncode, 0)
        finally:
            conn.close()
            upstream.close()

    def test_all_21_listeners_are_ready_and_config_change_is_detected(self):
        reserved = []
        try:
            for _ in range(21):
                sock = socket.socket()
                sock.bind(('127.0.0.1', 0))
                reserved.append(sock)
            ports = [sock.getsockname()[1] for sock in reserved]
        finally:
            for sock in reserved:
                sock.close()
        self.write_config([self.route(port, 1) for port in ports])
        result = self.run_cli('up')
        self.assertEqual(result.returncode, 0, result.stderr)
        for port in ports:
            with socket.create_connection(('127.0.0.1', port), timeout=2):
                pass
        self.write_config([self.route(ports[0], 2)])
        self.assertNotEqual(self.run_cli('status').returncode, 0)
        self.assertNotEqual(self.run_cli('up').returncode, 0)

    def test_bind_failure_does_not_leave_earlier_listener_running(self):
        with socket.socket() as occupied:
            occupied.bind(('127.0.0.1', 0))
            occupied.listen()
            second = occupied.getsockname()[1]
            first = free_port()
            self.write_config([self.route(first, 1), self.route(second, 1)])
            result = self.run_cli('up')
            self.assertNotEqual(result.returncode, 0)
            with socket.socket() as probe:
                probe.bind(('127.0.0.1', first))
            self.assertNotEqual(self.run_cli('status').returncode, 0)

    def test_refuses_to_stop_unrelated_process_with_forged_pid(self):
        self.write_config([self.route(free_port(), 1)])
        process = subprocess.Popen(['python3', '-B', '-c', 'import time; time.sleep(30)'], start_new_session=True)
        try:
            started = subprocess.check_output(['ps', '-p', str(process.pid), '-o', 'lstart='], text=True).strip()
            (self.dir / 'proxy-relay.pid.json').write_text(json.dumps({'pid': process.pid, 'token': 'a' * 32, 'started': started}))
            self.run_cli('stop')
            self.assertIsNone(process.poll())
        finally:
            process.terminate()
            process.wait(timeout=5)

    def test_different_configs_in_one_directory_cannot_displace_running_relay(self):
        port = free_port()
        self.write_config([self.route(port, 1)])
        other = self.dir / 'other.json'
        other.write_text(json.dumps({'routes': [self.route(free_port(), 1)]}))
        started = self.run_cli('up')
        self.assertEqual(started.returncode, 0, started.stderr)
        lease = (self.dir / 'proxy-relay.pid.json').read_bytes()
        for action in ('stop', 'up'):
            result = subprocess.run(['python3', '-B', str(SCRIPT), action, '--config', str(other)],
                                    capture_output=True, text=True, timeout=15)
            self.assertNotEqual(result.returncode, 0, result.stderr)
            self.assertEqual((self.dir / 'proxy-relay.pid.json').read_bytes(), lease)
            self.assertEqual(self.run_cli('status').returncode, 0)
        self.assertEqual(self.run_cli('stop').returncode, 0)
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', port))

    def test_initial_identity_lookup_failure_reaps_new_child(self):
        port = free_port()
        self.write_config([self.route(port, 1)])
        runner = """
import importlib.util, sys, time
spec = importlib.util.spec_from_file_location('relay', sys.argv[1])
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)
relay.process_info = lambda pid: (time.sleep(0.5), (None, []))[1]
sys.argv = [sys.argv[1], 'up', '--config', sys.argv[2]]
sys.exit(relay.main())
"""
        result = subprocess.run([sys.executable, '-B', '-c', runner, str(SCRIPT), str(self.config)],
                                capture_output=True, text=True, timeout=15)
        self.assertNotEqual(result.returncode, 0)
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', port))
        self.assertFalse((self.dir / 'proxy-relay.pid.json').exists())

    def test_identity_lookup_failure_does_not_replace_existing_lease(self):
        self.write_config([self.route(free_port(), 1)])
        self.assertEqual(self.run_cli('up').returncode, 0)
        lease = (self.dir / 'proxy-relay.pid.json').read_bytes()
        runner = """
import importlib.util, sys
spec = importlib.util.spec_from_file_location('relay', sys.argv[1])
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)
relay.process_info = lambda pid: (None, [])
sys.argv = [sys.argv[1], sys.argv[3], '--config', sys.argv[2]]
sys.exit(relay.main())
"""
        for action in ('up', 'stop'):
            result = subprocess.run([sys.executable, '-B', '-c', runner, str(SCRIPT), str(self.config), action],
                                    capture_output=True, text=True, timeout=15)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual((self.dir / 'proxy-relay.pid.json').read_bytes(), lease)
        self.assertEqual(self.run_cli('status').returncode, 0)

    def test_lease_write_failure_reaps_new_child(self):
        port = free_port()
        self.write_config([self.route(port, 1)])
        runner = """
import importlib.util, sys, time
spec = importlib.util.spec_from_file_location('relay', sys.argv[1])
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)
def fail_write(path, value):
    time.sleep(0.5)
    raise OSError('simulated failure')
relay.atomic_json = fail_write
sys.argv = [sys.argv[1], 'up', '--config', sys.argv[2]]
sys.exit(relay.main())
"""
        result = subprocess.run([sys.executable, '-B', '-c', runner, str(SCRIPT), str(self.config)],
                                capture_output=True, text=True, timeout=15)
        self.assertNotEqual(result.returncode, 0)
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', port))
        self.assertFalse((self.dir / 'proxy-relay.pid.json').exists())

    def test_readiness_failure_reaps_new_child(self):
        port = free_port()
        self.write_config([self.route(port, 1)])
        runner = """
import importlib.util, sys
spec = importlib.util.spec_from_file_location('relay', sys.argv[1])
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)
relay.ready = lambda *args: False
sys.argv = [sys.argv[1], 'up', '--config', sys.argv[2]]
sys.exit(relay.main())
"""
        result = subprocess.run([sys.executable, '-B', '-c', runner, str(SCRIPT), str(self.config)],
                                capture_output=True, text=True, timeout=15)
        self.assertNotEqual(result.returncode, 0)
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', port))
        self.assertFalse((self.dir / 'proxy-relay.pid.json').exists())

    def test_stop_keeps_lease_when_process_identity_becomes_unavailable(self):
        self.write_config([self.route(free_port(), 1)])
        self.assertEqual(self.run_cli('up').returncode, 0)
        lease_path = self.dir / 'proxy-relay.pid.json'
        lease = lease_path.read_bytes()
        pid = json.loads(lease)['pid']
        os.kill(pid, signal.SIGSTOP)
        try:
            runner = """
import importlib.util, sys
spec = importlib.util.spec_from_file_location('relay', sys.argv[1])
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)
real = relay.process_info
calls = [0]
def info(pid):
    calls[0] += 1
    return real(pid) if calls[0] <= 3 else (None, [])
relay.process_info = info
sys.argv = [sys.argv[1], 'stop', '--config', sys.argv[2]]
sys.exit(relay.main())
"""
            result = subprocess.run([sys.executable, '-B', '-c', runner, str(SCRIPT), str(self.config)],
                                    capture_output=True, text=True, timeout=15)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(lease_path.read_bytes(), lease)
        finally:
            try:
                os.kill(pid, signal.SIGCONT)
            except ProcessLookupError:
                pass

    @unittest.skipUnless(sys.platform == 'darwin', 'IPV6_BOUND_IF is a macOS option')
    def test_forwards_through_bound_loopback_interface(self):
        upstream = socket.socket(socket.AF_INET6)
        upstream.bind(('::1', 0))
        upstream.listen()
        upstream.settimeout(5)
        port = free_port()
        self.write_config([self.route(port, upstream.getsockname()[1])], upstream_interface='lo0')
        received = []

        def respond():
            try:
                conn, _ = upstream.accept()
                with conn:
                    received.append(conn.recv(4096))
                    conn.sendall(b'bound relay response')
            finally:
                upstream.close()

        thread = threading.Thread(target=respond, daemon=True)
        thread.start()
        result = self.run_cli('up')
        self.assertEqual(result.returncode, 0, result.stderr)
        with socket.create_connection(('127.0.0.1', port), timeout=3) as conn:
            conn.sendall(b'bound relay request')
            self.assertEqual(conn.recv(4096), b'bound relay response')
        thread.join(5)
        self.assertEqual(received, [b'bound relay request'])

    @unittest.skipUnless(sys.platform == 'darwin', 'IPV6_BOUND_IF is a macOS option')
    def test_bound_socket_is_used_and_binding_failure_never_falls_back(self):
        runner = """
import asyncio, importlib.util, socket, sys
from pathlib import Path
spec = importlib.util.spec_from_file_location('relay', sys.argv[1])
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)
original = socket.socket
class CheckedSocket(original):
    def setsockopt(self, level, option, value):
        if level == socket.IPPROTO_IPV6 and option == 125 and sys.argv[3] == 'reject':
            raise OSError('simulated interface binding failure')
        return super().setsockopt(level, option, value)
    def connect(self, address):
        if self.family == socket.AF_INET6 and address[0] == '::1':
            if self.getsockopt(socket.IPPROTO_IPV6, 125) != socket.if_nametoindex('lo0'):
                raise OSError('outgoing socket was not bound to lo0')
        return super().connect(address)
socket.socket = CheckedSocket
config = Path(sys.argv[2])
asyncio.run(relay.serve(config, 'a' * 32, config.parent / 'proxy-relay.ready.json'))
"""
        for mode in ('verify', 'reject'):
            with self.subTest(mode=mode), socket.socket(socket.AF_INET6) as upstream:
                upstream.bind(('::1', 0))
                upstream.listen()
                upstream.settimeout(2)
                port = free_port()
                self.write_config([self.route(port, upstream.getsockname()[1])], upstream_interface='lo0')
                marker = self.dir / 'proxy-relay.ready.json'
                process = subprocess.Popen([sys.executable, '-B', '-c', runner, str(SCRIPT),
                                            str(self.config), mode], stdout=subprocess.DEVNULL,
                                           stderr=subprocess.DEVNULL)
                try:
                    deadline = time.monotonic() + 5
                    while not marker.exists() and process.poll() is None and time.monotonic() < deadline:
                        time.sleep(0.02)
                    self.assertTrue(marker.exists(), 'relay did not start')
                    with socket.create_connection(('127.0.0.1', port), timeout=3) as client:
                        client.sendall(b'probe')
                        if mode == 'verify':
                            conn, _ = upstream.accept()
                            with conn:
                                self.assertEqual(conn.recv(5), b'probe')
                                conn.sendall(b'confirmed')
                                self.assertEqual(client.recv(9), b'confirmed')
                        else:
                            try:
                                self.assertEqual(client.recv(1), b'')
                            except ConnectionResetError:
                                pass
                            with self.assertRaises(socket.timeout):
                                upstream.accept()
                finally:
                    process.terminate()
                    process.wait(timeout=5)
                    marker.unlink(missing_ok=True)

    def test_rejects_invalid_interface_before_starting(self):
        port = free_port()
        for interface in ('', 42, None, 'no_such_interface_4832'):
            self.write_config([self.route(port, 1)], upstream_interface=interface)
            self.assertNotEqual(self.run_cli('up').returncode, 0)
            self.assertFalse((self.dir / 'proxy-relay.pid.json').exists())
            with socket.socket() as probe:
                probe.bind(('127.0.0.1', port))

    def test_configured_interface_fails_closed_on_unsupported_platform(self):
        self.write_config([self.route(free_port(), 1)], upstream_interface='lo0')
        runner = """
import importlib.util, sys
spec = importlib.util.spec_from_file_location('relay', sys.argv[1])
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)
relay.sys.platform = 'linux'
try:
    relay.load_config(__import__('pathlib').Path(sys.argv[2]))
except ValueError as exc:
    print(exc)
    sys.exit(1)
sys.exit(0)
"""
        result = subprocess.run([sys.executable, '-B', '-c', runner, str(SCRIPT), str(self.config)],
                                capture_output=True, text=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('macOS', result.stdout)

    def test_rejects_duplicate_ports_and_non_loopback_binding(self):
        port = free_port()
        for config in [
            {'routes': [self.route(port, 1), self.route(port, 2)]},
            {'routes': [self.route(port, 1)], 'listen_host': '0.0.0.0'},
            {'routes': [self.route(port, 1)], 'password': 'secret'},
            {'routes': [{'listen_port': port, 'upstream_host': 'example.com', 'upstream_port': 1}]},
            {'routes': [{'listen_port': port, 'upstream_host': 1, 'upstream_port': 1}]},
        ]:
            self.config.write_text(json.dumps(config))
            self.assertNotEqual(self.run_cli('up').returncode, 0)
            with socket.socket() as probe:
                probe.bind(('127.0.0.1', port))


if __name__ == '__main__':
    unittest.main()
