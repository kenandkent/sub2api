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

FAKE_DOCKER = r'''#!/usr/bin/env python3
import json, os, sys, time
from pathlib import Path
args = sys.argv[1:]
log = Path(os.environ["FAKE_LOG"])
records = json.loads(log.read_text()) if log.exists() else []
record = {"args": args, "image": os.environ.get("SUB2API_IMAGE", ""), "config": os.environ.get("DOCKER_CONFIG", "")}
records.append(record)
log.write_text(json.dumps(records))
scenario = os.environ.get("SCENARIO", "success")
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
    if scenario == "pull-fail" or (scenario == "pull-retry" and attempts < 3):
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
if "exec" in args and "sub2api" in args:
    if "setup/status" in " ".join(args):
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
            for filename in ("docker-compose.local.yml", "docker-compose.production.yml"):
                (directory / filename).write_text("services: {}\n")
        self.state = self.root / "current-deployment"
        self.state.write_text(OLD + " " + OLD_IMAGE + "\n")

    def tearDown(self):
        self.temp.cleanup()

    def run_deploy(self, scenario="success", image=NEW_IMAGE, login=False):
        env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                   FAKE_LOG=str(self.log), SCENARIO=scenario,
                   DEPLOY_RETRY_DELAY_SECONDS="0", PYTHONDONTWRITEBYTECODE="1")
        env["DEPLOY_PULL_TIMEOUT_SECONDS"] = "1"
        args = ["bash", str(SCRIPT), str(self.root), NEW, image]
        if login:
            args.append("kenandkent")
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
        self.assertEqual(self.state.read_text().strip(), NEW + " " + NEW_IMAGE)
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


if __name__ == "__main__":
    unittest.main()
