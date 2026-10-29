"""Exercise deployment failure paths without contacting a server or registry."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "deploy/deploy-mysubapi.sh"
OLD = "a" * 40 + "-10-1"
NEW = "b" * 40 + "-20-1"
OLD_IMAGE = "ghcr.io/kenandkent/sub2api@sha256:" + "1" * 64
NEW_IMAGE = "ghcr.io/kenandkent/sub2api@sha256:" + "2" * 64
OLD_WORKER = "ghcr.io/kenandkent/sub2api-opencode-free-worker@sha256:" + "3" * 64
NEW_WORKER = "ghcr.io/kenandkent/sub2api-opencode-free-worker@sha256:" + "4" * 64

FAKE_DOCKER = r'''#!/usr/bin/env python3
import fcntl, json, os, sys, time
from pathlib import Path
args = sys.argv[1:]
log = Path(os.environ["FAKE_LOG"])
record = {"args": args, "image": os.environ.get("SUB2API_IMAGE", ""), "config": os.environ.get("DOCKER_CONFIG", ""), "worker": os.environ.get("OPENCODE_FREE_WORKER_IMAGE", "")}
with log.open("a+") as file:
    fcntl.flock(file, fcntl.LOCK_EX)
    file.seek(0)
    records = json.loads(file.read() or "[]")
    records.append(record)
    file.seek(0)
    file.truncate()
    file.write(json.dumps(records))
    file.flush()
    fcntl.flock(file, fcntl.LOCK_UN)
scenario = os.environ.get("SCENARIO", "success")
if args[:2] == ["context", "show"]:
    print("remote" if scenario == "custom-context" else "default")
    sys.exit(0)
if args[0] == "compose" and scenario == "user-plugin":
    config = Path(record["config"] or os.environ["HOME"] + "/.docker")
    if not (config / "cli-plugins" / "docker-compose").exists():
        sys.exit(1)
if scenario == "missing-token" and "rm" in args and args[-1] == "opencode-free-worker" and not os.environ.get("OPENCODE_FREE_BUN_TOKEN"):
    sys.exit(1)
if "config" in args and "--quiet" in args and scenario == "old-config-fail" and any("a" * 40 in arg for arg in args):
    sys.exit(1)
if "config" in args and "--environment" in args:
    print("OPENCODE_FREE_BUN_TOKEN=" + ("" if scenario == "missing-token" else "short" if scenario == "short-token" else "é" * 32 if scenario == "nonascii-token" else "t" * 32))
    sys.exit(0)
if args[:2] == ["login", "ghcr.io"]:
    assert sys.stdin.read().strip() == "private-token-sentinel"
    assert Path(record["config"]).is_dir()
    sys.exit(0)
if args[:3] == ["compose", "version", "--short"]:
    print("2.23.0" if scenario == "old-compose" else "2.24.4")
    sys.exit(0)
if "pull" in args:
    if scenario == "pull-timeout" and args[-1] == "sub2api":
        time.sleep(10)
    attempts = sum("pull" in r["args"] and r["args"][-1] == args[-1] for r in records)
    if scenario == "pull-fail" or (scenario == "worker-pull-fail" and args[-1] == "opencode-free-worker") or (scenario == "pull-retry" and attempts < 3):
        sys.exit(1)
if "exec" in args and "postgres" in args:
    if "pg_dump" in " ".join(args):
        if scenario == "backup-fail":
            sys.exit(1)
        print("PGDMP-private-database-sentinel")
    elif scenario == "sql-fail":
        sys.exit(1)
    else:
        print("1")
if "up" in args and args[-1] == "sub2api":
    if scenario in ("up-fail", "rollback-fail") and record["image"].endswith("2" * 64):
        sys.exit(1)
    if scenario == "rollback-fail" and record["image"].endswith("1" * 64):
        sys.exit(1)
if "up" in args and args[-1] == "opencode-free-worker" and scenario == "worker-up-fail" and record["worker"].endswith("4" * 64):
    sys.exit(1)
if "exec" in args and "opencode-free-worker" in args:
    token = sys.stdin.read()
    if token != "t" * 32 or (scenario == "worker-probe-fail" and record["worker"].endswith("4" * 64)):
        sys.exit(1)
if "exec" in args and "sub2api" in args:
    if "OPENCODE_FREE_BUN_TOKEN" in " ".join(args):
        if record["image"].endswith("2" * 64) and scenario == "app-token-wrong":
            print("w" * 32, end="")
        elif record["image"].endswith("2" * 64) and scenario == "app-token-missing":
            pass
        else:
            print("t" * 32, end="")
    elif "setup/status" in " ".join(args):
        print('{"code":0,"data":{"needs_setup":' + ("true" if scenario == "setup-fail" and record["image"].endswith("2" * 64) else "false") + '}}')
    elif "settings/public" in " ".join(args):
        if scenario == "public-fail" and record["image"].endswith("2" * 64):
            sys.exit(1)
        print('{"code":0,"data":{"site_name":"Local"}}')
'''


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        (ROOT / ".cache").mkdir(exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(prefix="deploy-test-", dir=ROOT / ".cache")
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        docker = self.bin / "docker"
        docker.write_text(FAKE_DOCKER)
        docker.chmod(0o755)
        timeout = self.bin / "timeout"
        timeout.write_text("#!/usr/bin/env python3\nimport subprocess, sys\ntry:\n    sys.exit(subprocess.run(sys.argv[4:], timeout=float(sys.argv[3])).returncode)\nexcept subprocess.TimeoutExpired:\n    sys.exit(124)\n")
        timeout.chmod(0o755)
        self.log = self.root / "commands.json"
        (self.root / ".env").write_text("POSTGRES_PASSWORD=private-env-sentinel\n")
        for release in (OLD, NEW):
            directory = self.root / "releases" / release
            directory.mkdir(parents=True)
            for filename in ("docker-compose.local.yml", "docker-compose.production.yml", "docker-compose.opencode-free.production.yml"):
                (directory / filename).write_text("services: {}\n")
        self.state = self.root / "current-deployment"
        self.state.write_text(OLD + " " + OLD_IMAGE + "\n")

    def tearDown(self):
        self.temp.cleanup()

    def run_deploy(self, scenario="success", image=NEW_IMAGE, login=False, worker=None):
        env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                   FAKE_LOG=str(self.log), SCENARIO=scenario,
                   DEPLOY_RETRY_DELAY_SECONDS="0", PYTHONDONTWRITEBYTECODE="1")
        env["DEPLOY_PULL_TIMEOUT_SECONDS"] = "1"
        env["HOME"] = str(self.root / "home")
        env.pop("DOCKER_CONFIG", None)
        env.pop("DOCKER_CONTEXT", None)
        if scenario == "user-plugin":
            plugin = Path(env["HOME"]) / ".docker/cli-plugins/docker-compose"
            plugin.parent.mkdir(parents=True, exist_ok=True)
            plugin.write_text("fake plugin")
        args = ["bash", str(SCRIPT), str(self.root), NEW, image]
        if login or worker is not None:
            args.append("kenandkent" if login else "")
        if worker is not None:
            args.append(worker)
        result = subprocess.run(args, input="private-token-sentinel\n" if login else "",
                                text=True, capture_output=True, env=env)
        self.assertNotIn("private-token-sentinel", result.stdout + result.stderr)
        self.assertNotIn("private-env-sentinel", result.stdout + result.stderr)
        self.assertFalse((self.root / ".deploy-lock").exists())
        self.assertEqual(list(self.root.glob(".registry-auth.*")), [])
        self.assertTrue(SCRIPT.is_file(), "deployment helper must exist")
        return result

    def records(self):
        return json.loads(self.log.read_text()) if self.log.exists() else []

    def app_updates(self):
        return [r for r in self.records() if "up" in r["args"] and r["args"][-1] == "sub2api"]

    def test_success_backs_up_before_app_and_commits_state_after_probes(self):
        result = self.run_deploy(login=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.state.read_text().strip(), NEW + " " + NEW_IMAGE + " -")
        calls = self.records()
        backup = next(i for i, r in enumerate(calls) if "pg_dump" in " ".join(r["args"]))
        update = next(i for i, r in enumerate(calls) if "up" in r["args"] and r["args"][-1] == "sub2api")
        self.assertLess(backup, update)
        self.assertTrue(any("setup/status" in " ".join(r["args"]) for r in calls[update:]))
        self.assertTrue(any("settings/public" in " ".join(r["args"]) for r in calls[update:]))
        backups = list((self.root / "backups").glob("*.dump"))
        self.assertEqual(len(backups), 1)
        self.assertTrue(backups[0].read_bytes().startswith(b"PGDMP"))
        self.assertEqual(backups[0].stat().st_mode & 0o077, 0)
        auth = next(r["config"] for r in calls if r["args"][0] == "login")
        self.assertTrue(Path(auth).is_relative_to(self.root) if hasattr(Path(auth), "is_relative_to") else str(auth).startswith(str(self.root)))

    def test_download_is_retried(self):
        result = self.run_deploy("pull-retry")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sum("pull" in r["args"] and r["args"][-1] == "sub2api" for r in self.records()), 3)

    def test_pre_apply_failures_do_not_replace_current_app(self):
        for scenario in ("pull-fail", "sql-fail", "backup-fail", "old-compose"):
            with self.subTest(scenario=scenario):
                self.log.unlink(missing_ok=True)
                result = self.run_deploy(scenario)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.app_updates(), [])
                self.assertEqual(self.state.read_text().strip(), OLD + " " + OLD_IMAGE)

    def test_stalled_pull_is_bounded_and_does_not_replace_app(self):
        result = self.run_deploy("pull-timeout")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(sum("pull" in r["args"] and r["args"][-1] == "sub2api" for r in self.records()), 3)
        self.assertEqual(self.app_updates(), [])
        self.assertEqual(self.state.read_text().strip(), OLD + " " + OLD_IMAGE)

    def test_failed_apply_restores_previous_release_and_still_fails(self):
        for scenario in ("up-fail", "setup-fail", "public-fail"):
            with self.subTest(scenario=scenario):
                self.log.unlink(missing_ok=True)
                result = self.run_deploy(scenario)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual([r["image"] for r in self.app_updates()], [NEW_IMAGE, OLD_IMAGE])
                self.assertEqual(self.state.read_text().strip(), OLD + " " + OLD_IMAGE)
                rollback = self.app_updates()[-1]
                self.assertTrue(any(OLD in value for value in rollback["args"]))

    def test_failed_restore_and_first_deployment_failure_are_not_success(self):
        result = self.run_deploy("rollback-fail")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("restore", result.stderr.lower())
        self.state.unlink()
        self.log.unlink()
        result = self.run_deploy("up-fail")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.state.exists())
        self.assertEqual(len(self.app_updates()), 1)

    def test_invalid_digest_and_missing_env_are_rejected_before_docker(self):
        result = self.run_deploy(image="ghcr.io/kenandkent/sub2api:mysubapi")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.records(), [])
        (self.root / ".env").unlink()
        result = self.run_deploy()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.records(), [])

    def test_existing_lock_is_not_removed_or_used(self):
        lock = self.root / ".deploy-lock"
        lock.mkdir()
        env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"], FAKE_LOG=str(self.log))
        result = subprocess.run(["bash", str(SCRIPT), str(self.root), NEW, NEW_IMAGE],
                                text=True, capture_output=True, env=env)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(lock.exists())
        self.assertEqual(self.records(), [])


    def test_worker_success_uses_matching_digests_and_recreates_after_app(self):
        result = self.run_deploy(worker=NEW_WORKER)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.state.read_text().strip(), NEW + " " + NEW_IMAGE + " " + NEW_WORKER)
        calls = self.records()
        app = next(i for i, r in enumerate(calls) if "up" in r["args"] and r["args"][-1] == "sub2api")
        worker = next(i for i, r in enumerate(calls) if "up" in r["args"] and r["args"][-1] == "opencode-free-worker")
        self.assertGreater(worker, app)
        self.assertIn("--force-recreate", calls[worker]["args"])
        self.assertIn("--wait", calls[worker]["args"])
        self.assertEqual(calls[worker]["image"], NEW_IMAGE)
        self.assertEqual(calls[worker]["worker"], NEW_WORKER)
        self.assertTrue(any("exec" in r["args"] and "opencode-free-worker" in r["args"] and "/v1/transport/chat" in " ".join(r["args"]) and "x-sub2api-dispatched" in " ".join(r["args"]) for r in calls[worker:]))
        self.assertTrue(any("pull" in r["args"] and r["args"][-1] == "opencode-free-worker" for r in calls[:app]))

    def test_worker_preflight_failure_preserves_app_and_state(self):
        for scenario in ("missing-token", "short-token", "nonascii-token", "worker-pull-fail"):
            with self.subTest(scenario=scenario):
                self.log.unlink(missing_ok=True)
                result = self.run_deploy(scenario, worker=NEW_WORKER)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.app_updates(), [])
                self.assertEqual(self.state.read_text().strip(), OLD + " " + OLD_IMAGE)
        self.log.unlink(missing_ok=True)
        result = self.run_deploy(worker="ghcr.io/kenandkent/sub2api-opencode-free-worker:latest")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.app_updates(), [])
        (self.root / "releases" / NEW / "docker-compose.opencode-free.production.yml").unlink()
        result = self.run_deploy(worker=NEW_WORKER)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.app_updates(), [])

    def test_failed_worker_restores_previous_pair(self):
        for scenario in ("worker-up-fail", "worker-probe-fail"):
            with self.subTest(scenario=scenario):
                self.log.unlink(missing_ok=True)
                self.state.write_text(OLD + " " + OLD_IMAGE + " " + OLD_WORKER + "\n")
                result = self.run_deploy(scenario, worker=NEW_WORKER)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual([r["image"] for r in self.app_updates()], [NEW_IMAGE, OLD_IMAGE])
                workers = [r for r in self.records() if "up" in r["args"] and r["args"][-1] == "opencode-free-worker"]
                self.assertEqual([r["worker"] for r in workers], [NEW_WORKER, OLD_WORKER])
                self.assertIn("--force-recreate", workers[-1]["args"])
                self.assertEqual(self.state.read_text().strip(), OLD + " " + OLD_IMAGE + " " + OLD_WORKER)

    def test_disabling_worker_removes_old_worker_after_app_health(self):
        self.state.write_text(OLD + " " + OLD_IMAGE + " " + OLD_WORKER + "\n")
        result = self.run_deploy(worker="-")
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.records()
        probe = next(i for i, r in enumerate(calls) if "settings/public" in " ".join(r["args"]))
        removal = next(i for i, r in enumerate(calls) if "rm" in r["args"] and r["args"][-1] == "opencode-free-worker")
        self.assertGreater(removal, probe)
        self.assertEqual(self.state.read_text().strip(), NEW + " " + NEW_IMAGE + " -")

    def test_disabling_worker_requires_old_configuration_to_remain_recoverable(self):
        self.state.write_text(OLD + " " + OLD_IMAGE + " " + OLD_WORKER + "\n")
        for scenario in ("missing-token", "short-token", "nonascii-token", "old-config-fail"):
            with self.subTest(scenario=scenario):
                self.log.unlink(missing_ok=True)
                result = self.run_deploy(scenario, worker="-")
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.app_updates(), [])
                self.assertEqual(self.state.read_text().strip(), OLD + " " + OLD_IMAGE + " " + OLD_WORKER)

    def test_worker_probe_rejects_incorrect_or_missing_app_token_and_restores_old_pair(self):
        self.state.write_text(OLD + " " + OLD_IMAGE + " " + OLD_WORKER + "\n")
        for scenario in ("app-token-wrong", "app-token-missing"):
            with self.subTest(scenario=scenario):
                self.log.unlink(missing_ok=True)
                result = self.run_deploy(scenario, worker=NEW_WORKER)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual([r["image"] for r in self.app_updates()], [NEW_IMAGE, OLD_IMAGE])
                workers = [r for r in self.records() if "up" in r["args"] and r["args"][-1] == "opencode-free-worker"]
                self.assertEqual([r["worker"] for r in workers], [NEW_WORKER, OLD_WORKER])
                self.assertEqual(self.state.read_text().strip(), OLD + " " + OLD_IMAGE + " " + OLD_WORKER)
                self.assertNotIn("t" * 32, result.stdout + result.stderr)
                self.assertNotIn("w" * 32, result.stdout + result.stderr)
                self.assertNotIn("t" * 32, self.log.read_text())
                self.assertNotIn("w" * 32, self.log.read_text())

    def test_failed_worker_restoring_old_without_worker_removes_target(self):
        result = self.run_deploy("worker-probe-fail", worker=NEW_WORKER)
        self.assertNotEqual(result.returncode, 0)
        calls = self.records()
        old_app = next(i for i, r in enumerate(calls) if "up" in r["args"] and r["args"][-1] == "sub2api" and r["image"] == OLD_IMAGE)
        self.assertTrue(any("rm" in r["args"] and r["args"][-1] == "opencode-free-worker" for r in calls[old_app:]))
        self.assertEqual(self.state.read_text().strip(), OLD + " " + OLD_IMAGE)

    def test_state_extra_fields_rejected_before_app(self):
        self.state.write_text(OLD + " " + OLD_IMAGE + " - unexpected\n")
        result = self.run_deploy(worker=NEW_WORKER)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.app_updates(), [])

    def test_user_compose_plugin_survives_private_login_configuration(self):
        result = self.run_deploy("user-plugin", login=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_nondefault_context_is_rejected_before_app(self):
        result = self.run_deploy("custom-context", login=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.app_updates(), [])


if __name__ == "__main__":
    unittest.main()
