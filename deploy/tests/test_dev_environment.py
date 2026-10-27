"""Local development isolation and supervisor lifecycle tests."""
import importlib.util
import json
import os
import socket
from pathlib import Path
import subprocess
import tempfile
import time
import threading
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SLEEPER = "import signal,time; signal.signal(signal.SIGINT,signal.SIG_DFL); time.sleep(60)"
SPEC = importlib.util.spec_from_file_location("local_dev", ROOT / "deploy/dev.py")
DEV = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DEV)


class DevEnvironmentTests(unittest.TestCase):
    def setUp(self):
        (ROOT / ".cache").mkdir(exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(prefix="dev-test-", dir=ROOT / ".cache")
        self.root = Path(self.temp.name)
        (self.root / ".dev").mkdir()
        (self.root / "backend").mkdir()
        (self.root / "backend/go.mod").write_text("module example\n\ngo 1.27.3\n")

    def tearDown(self):
        self.temp.cleanup()

    def test_initialize_is_private_and_preserves_existing_configuration(self):
        settings = DEV.initialize(self.root)
        self.assertEqual(settings["DEV_BACKEND_PORT"], "18080")
        self.assertEqual(settings["DEV_POSTGRES_PORT"], "15433")
        self.assertGreaterEqual(len(settings["DEV_ADMIN_PASSWORD"]), 24)
        env_path = self.root / ".dev/.env"
        self.assertEqual(env_path.stat().st_mode & 0o077, 0)
        env_path.write_text(env_path.read_text().replace("DEV_FRONTEND_PORT=3000", "DEV_FRONTEND_PORT=3300"))
        self.assertEqual(DEV.initialize(self.root)["DEV_FRONTEND_PORT"], "3300")
        self.assertEqual(DEV.initialize(self.root)["DEV_ADMIN_PASSWORD"], settings["DEV_ADMIN_PASSWORD"])

    def test_child_environment_cannot_reuse_full_deployment_paths_or_database(self):
        settings = DEV.initialize(self.root)
        with mock.patch.dict(os.environ, {"DATA_DIR": "/outside", "CONFIG_FILE": "/outside/config.yaml",
                                         "DATABASE_HOST": "production", "GOCACHE": "/outside/cache",
                                         "LOG_OUTPUT_FILE_PATH": "/outside/logs/app.log",
                                         "PLUGINS_DATA_DIR": "/outside/plugins",
                                         "PAYMENT_LOCAL_PATH": "/outside/unknown-app-setting"}):
            env = DEV.development_env(self.root, settings)
        self.assertEqual(env["DATABASE_HOST"], "127.0.0.1")
        self.assertEqual(env["DATABASE_DBNAME"], "sub2api_dev")
        self.assertEqual(env["DATABASE_PORT"], "15433")
        self.assertEqual(env["GOTOOLCHAIN"], "go1.27.3")
        self.assertEqual(env["VITE_DEV_PROXY_TARGET"], "http://127.0.0.1:18080")
        self.assertEqual(env["VITE_API_BASE_URL"], "/api/v1")
        self.assertTrue("PAYMENT_LOCAL_PATH" not in env, "ambient application settings must be discarded")
        for key in ("DATA_DIR", "CONFIG_FILE", "PRICING_DATA_DIR", "GOCACHE", "GOMODCACHE",
                    "GOPATH", "GOBIN", "TMPDIR", "GOTMPDIR", "COREPACK_HOME", "XDG_CACHE_HOME",
                    "XDG_DATA_HOME", "PNPM_HOME", "npm_config_store_dir", "LOG_OUTPUT_FILE_PATH",
                    "PLUGINS_DATA_DIR"):
            self.assertTrue(Path(env[key]).is_relative_to(self.root), key)
        # The override file must not prevent AUTO_SETUP from writing config.yaml.
        self.assertNotEqual(Path(env["CONFIG_FILE"]).name, "config.yaml")
        self.assertIn('remote_url: ""', Path(env["CONFIG_FILE"]).read_text())
        self.assertFalse((self.root / ".dev/backend/config.yaml").exists())

    def test_invalid_configuration_is_rejected_without_shell_evaluation(self):
        settings = DEV.initialize(self.root)
        settings["DEV_BACKEND_PORT"] = "8080; touch /outside"
        with self.assertRaises(ValueError):
            DEV.development_env(self.root, settings)

    def test_cleanup_stops_own_process_group_only(self):
        own = subprocess.Popen(["/usr/bin/python3", "-c", SLEEPER], start_new_session=True, stderr=subprocess.DEVNULL)
        other = subprocess.Popen(["/usr/bin/python3", "-c", SLEEPER], start_new_session=True, stderr=subprocess.DEVNULL)
        try:
            DEV.stop_children([own], timeout=2)
            self.assertIsNotNone(own.poll())
            self.assertIsNone(other.poll())
        finally:
            DEV.stop_children([own, other], timeout=2)

    def test_foreign_or_stale_supervisor_is_not_signalled(self):
        process = subprocess.Popen(["/usr/bin/python3", "-c", SLEEPER], start_new_session=True, stderr=subprocess.DEVNULL)
        try:
            state = {"pid": process.pid, "started": "not-the-real-start-time", "root": str(self.root)}
            self.assertFalse(DEV.is_our_supervisor(state, self.root))
            self.assertIsNone(process.poll())
        finally:
            DEV.stop_children([process], timeout=2)

    def test_cancelled_setup_reaps_its_tool_process(self):
        marker = self.root / "installer.pid"
        command = ["/usr/bin/python3", "-c",
                   "import os,time,signal; signal.signal(signal.SIGINT, signal.SIG_DFL); open(" + repr(str(marker)) + ", 'w').write(str(os.getpid())); time.sleep(60)"]
        timer = threading.Timer(0.3, DEV.CANCEL.set)
        timer.start()
        try:
            with self.assertRaises(InterruptedError):
                DEV.retry_run(command, dict(os.environ), self.root, 30)
            self.assertEqual(DEV.process_start(int(marker.read_text())), "")
        finally:
            timer.join()
            DEV.CANCEL.clear()

    def test_cleanup_stops_group_members_after_leader_has_exited(self):
        marker = self.root / "grandchild.pid"
        child_code = "import signal,time; signal.signal(signal.SIGINT,signal.SIG_DFL); time.sleep(60)"
        parent_code = ("import subprocess; child=subprocess.Popen(['/usr/bin/python3','-c'," +
                       repr(child_code) + "]); open(" + repr(str(marker)) + ",'w').write(str(child.pid))")
        leader = subprocess.Popen(["/usr/bin/python3", "-c", parent_code], start_new_session=True)
        leader.wait(timeout=5)
        pid = int(marker.read_text())
        try:
            DEV.stop_children([leader], timeout=2)
            self.assertEqual(DEV.process_start(pid), "")
        finally:
            try:
                os.killpg(leader.pid, 9)
            except ProcessLookupError:
                pass

    def test_backend_record_cleans_air_separate_group_but_rejects_other_session(self):
        binary = self.root / ".cache/dev/bin/server"
        binary.parent.mkdir(parents=True, exist_ok=True)
        binary.write_text("#!/usr/bin/python3\nimport signal,time\nsignal.signal(signal.SIGINT,signal.SIG_DFL)\ntime.sleep(60)\n")
        binary.chmod(0o755)
        process = subprocess.Popen([str(binary)], start_new_session=True)
        record = self.root / ".dev/backend-process.json"
        record.write_text(json.dumps({"root": str(self.root), "pid": process.pid,
                                     "started": DEV.process_start(process.pid), "owner": "mine"}))
        try:
            DEV.stop_backend(self.root, "another-session")
            self.assertIsNone(process.poll())
            DEV.stop_backend(self.root, "mine")
            process.wait(timeout=3)
        finally:
            DEV.stop_children([process], timeout=2)

    def test_atomic_state_keeps_previous_json_if_replace_fails(self):
        path = self.root / ".dev/state.json"
        path.write_text('{"previous": true}')
        with mock.patch.object(DEV.os, "replace", side_effect=OSError("simulated interrupted update")):
            with self.assertRaises(OSError):
                DEV.atomic_state(path, {"new": True})
        self.assertEqual(DEV.read_state(path), {"previous": True})
        path.write_text('{"partial":')
        with self.assertRaisesRegex(ValueError, "inspect"):
            DEV.read_state(path)

    def test_recently_closed_development_port_can_be_reused(self):
        with socket.socket() as server:
            server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            server.bind(("127.0.0.1", 0))
            port = server.getsockname()[1]
            server.listen()
            self.assertFalse(DEV.available(str(port)), "a live listener must still be rejected")
            client = socket.create_connection(("127.0.0.1", port))
            accepted, _ = server.accept()
            accepted.close()
            client.recv(1)
            client.close()
        self.assertTrue(DEV.available(str(port)), "TIME_WAIT must not look like a live foreign listener")

    def test_stale_session_preserves_records_for_an_unverified_orphan_group(self):
        marker = self.root / "orphan.pid"
        code = ("import subprocess,time; p=subprocess.Popen(['/usr/bin/python3','-c'," +
                repr(SLEEPER) + "]); open(" + repr(str(marker)) + ",'w').write(str(p.pid)); time.sleep(0.4)")
        leader = subprocess.Popen(["/usr/bin/python3", "-c", code], start_new_session=True,
                                  stderr=subprocess.DEVNULL)
        started = DEV.process_start(leader.pid)
        leader.wait(timeout=3)
        state = self.root / ".dev/session.json"
        state.write_text(json.dumps({"root": str(self.root), "pid": 99999999, "started": "gone",
                                     "session": "orphan", "children": [{"pid": leader.pid, "started": started}]}))
        lock = self.root / ".dev/session.lock"
        lock.mkdir()
        try:
            with self.assertRaisesRegex(ValueError, "group"):
                DEV.stop(self.root)
            self.assertTrue(state.exists())
            self.assertTrue(lock.exists())
        finally:
            try:
                os.killpg(leader.pid, 9)
            except ProcessLookupError:
                pass


if __name__ == "__main__":
    unittest.main()
