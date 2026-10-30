"""Execute the real Actions SSH step with local SSH/SCP stand-ins."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]
APP = "ghcr.io/kenandkent/sub2api@sha256:" + "a" * 64
WORKER = "ghcr.io/kenandkent/sub2api-opencode-free-worker@sha256:" + "b" * 64


def deployment_step():
    lines = (ROOT / ".github/workflows/mysubapi-ci-cd.yml").read_text().splitlines()
    start = next(i for i, line in enumerate(lines) if "name: Upload release and deploy by digest" in line)
    start = next(i for i in range(start, len(lines)) if lines[i] == "        run: |") + 1
    end = next((i for i in range(start, len(lines)) if lines[i].strip() and not lines[i].startswith("          ")), len(lines))
    return textwrap.dedent("\n".join(lines[start:end]))


FAKE = '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
path = Path(os.environ["WORKFLOW_FAKE_LOG"])
records = json.loads(path.read_text()) if path.exists() else []
args = sys.argv[1:]
record = {"tool": Path(sys.argv[0]).name, "args": args}
if record["tool"] == "ssh":
    key = Path(args[args.index("-i") + 1])
    record["key_ok"] = key.read_text().strip() == "fixture-private-key-sentinel"
    record["key_private"] = key.stat().st_mode & 0o077 == 0
    known = next(x.split("=", 1)[1] for x in args if x.startswith("UserKnownHostsFile="))
    record["known_hosts"] = Path(known).read_text()
    if "deploy-mysubapi.sh" in args[-1]:
        record["token_ok"] = sys.stdin.read().strip() == "fixture-registry-token-sentinel"
records.append(record)
path.write_text(json.dumps(records))
if os.environ.get("WORKFLOW_FAKE_FAIL") == "true": sys.exit(1)
'''


class WorkflowDeployTests(unittest.TestCase):
    def setUp(self):
        (ROOT / ".cache").mkdir(exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(prefix="workflow-test-", dir=ROOT / ".cache")
        self.directory = Path(self.temp.name)
        binaries = self.directory / "bin"
        binaries.mkdir()
        for name in ("ssh", "scp"):
            executable = binaries / name
            executable.write_text(FAKE)
            executable.chmod(0o755)
        self.log = self.directory / "calls.json"
        self.env = dict(
            PATH=str(binaries) + os.pathsep + os.environ["PATH"], HOME=os.environ["HOME"],
            WORKFLOW_FAKE_LOG=str(self.log), PYTHONDONTWRITEBYTECODE="1",
            GITHUB_WORKSPACE=str(self.directory), GITHUB_STEP_SUMMARY=str(self.directory / "summary"),
            GITHUB_SHA="c" * 40, GITHUB_RUN_ID="123", GITHUB_RUN_ATTEMPT="2", GITHUB_ACTOR="fixture",
            SSH_PRIVATE_KEY="fixture-private-key-sentinel", SSH_KNOWN_HOSTS="",
            GHCR_TOKEN="fixture-registry-token-sentinel", DEPLOY_HOST="167.114.210.84",
            DEPLOY_PORT="22", DEPLOY_USER="root", DEPLOY_PATH="/home/workspace/subapi",
            DEPLOY_IMAGE=APP, DEPLOY_WORKER_IMAGE=WORKER,
        )

    def tearDown(self):
        self.temp.cleanup()

    def run_step(self, **values):
        result = subprocess.run(["bash", "-c", deployment_step()], cwd=ROOT,
                                env=dict(self.env, **values), text=True, capture_output=True, timeout=15)
        self.assertNotIn("fixture-private-key-sentinel", result.stdout + result.stderr)
        self.assertNotIn("fixture-registry-token-sentinel", result.stdout + result.stderr)
        self.assertEqual(list((self.directory / ".cache").glob("ssh.*")), [])
        return result

    def calls(self):
        return json.loads(self.log.read_text()) if self.log.exists() else []

    def test_existing_private_key_and_repository_pin_deploy_exact_digests(self):
        result = self.run_step()
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls()
        ssh = [r for r in calls if r["tool"] == "ssh"]
        self.assertTrue(all(r["key_ok"] and r["key_private"] for r in ssh))
        pin = (ROOT / "deploy/ssh/mysubapi_known_hosts").read_text()
        self.assertTrue(all(r["known_hosts"] == pin for r in ssh))
        self.assertTrue(all("StrictHostKeyChecking=yes" in r["args"] and
                            "GlobalKnownHostsFile=/dev/null" in r["args"] for r in calls))
        upload = next(r for r in calls if r["tool"] == "scp")
        self.assertIn("deploy/docker-compose.opencode-free.production.yml", upload["args"])
        self.assertIn("deploy/.env.mysubapi.production.example", upload["args"])
        self.assertIn("root@167.114.210.84:/home/workspace/subapi/incoming/" + "c" * 40 + "-123-2/", upload["args"])
        final = ssh[-1]
        self.assertTrue(final["token_ok"])
        self.assertIn("'" + APP + "'", final["args"][-1])
        self.assertIn("'" + WORKER + "'", final["args"][-1])

    def test_host_override_requires_explicit_trusted_key(self):
        result = self.run_step(DEPLOY_HOST="another.example.test")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.calls(), [])
        pin = "another.example.test ssh-ed25519 fixture-public-key\n"
        result = self.run_step(DEPLOY_HOST="another.example.test", SSH_KNOWN_HOSTS=pin)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(all(r["known_hosts"].strip() == pin.strip() for r in self.calls() if r["tool"] == "ssh"))

    def test_invalid_remote_path_or_mutable_image_never_contacts_server(self):
        for values in [dict(DEPLOY_PATH="/home/workspace/subapi/../other"),
                       dict(DEPLOY_IMAGE="ghcr.io/kenandkent/sub2api:mysubapi"),
                       dict(DEPLOY_WORKER_IMAGE="-"),
                       dict(DEPLOY_WORKER_IMAGE="ghcr.io/other/worker@sha256:" + "a" * 64),
                       dict(DEPLOY_WORKER_IMAGE="")]:
            with self.subTest(values=values):
                self.assertNotEqual(self.run_step(**values).returncode, 0)
                self.assertEqual(self.calls(), [])

    def test_upload_stops_when_initial_server_check_fails(self):
        result = self.run_step(WORKFLOW_FAKE_FAIL="true")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(self.calls()), 1)
        self.assertEqual(self.calls()[0]["tool"], "ssh")


def promotion_step():
    lines = (ROOT / ".github/workflows/mysubapi-ci-cd.yml").read_text().splitlines()
    start = next((i for i, line in enumerate(lines) if "name: Promote deployed images" in line), None)
    if start is None:
        return ""
    start = next(i for i in range(start, len(lines)) if lines[i] == "        run: |") + 1
    end = next((i for i in range(start, len(lines)) if lines[i].strip() and not lines[i].startswith("          ")), len(lines))
    return textwrap.dedent("\n".join(lines[start:end]))


DOCKER_FAKE = '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
path = Path(os.environ["WORKFLOW_FAKE_LOG"])
records = json.loads(path.read_text()) if path.exists() else []
records.append(sys.argv[1:])
path.write_text(json.dumps(records))
if os.environ.get("WORKFLOW_FAKE_FAIL") == "true": sys.exit(1)
'''


class WorkflowPromotionTests(unittest.TestCase):
    def setUp(self):
        (ROOT / ".cache").mkdir(exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(prefix="promotion-test-", dir=ROOT / ".cache")
        self.directory = Path(self.temp.name)
        binaries = self.directory / "bin"
        binaries.mkdir()
        docker = binaries / "docker"
        docker.write_text(DOCKER_FAKE)
        docker.chmod(0o755)
        self.log = self.directory / "calls.json"
        self.env = dict(PATH=str(binaries) + os.pathsep + os.environ["PATH"],
                        WORKFLOW_FAKE_LOG=str(self.log), PYTHONDONTWRITEBYTECODE="1",
                        DEPLOY_IMAGE=APP, DEPLOY_WORKER_IMAGE=WORKER,
                        GHCR_TOKEN="fixture-registry-token-sentinel")

    def tearDown(self):
        self.temp.cleanup()

    def run_step(self, **values):
        result = subprocess.run(["bash", "-c", promotion_step()], cwd=ROOT,
                                env=dict(self.env, **values), text=True, capture_output=True, timeout=15)
        self.assertNotIn("fixture-registry-token-sentinel", result.stdout + result.stderr)
        return result

    def calls(self):
        return json.loads(self.log.read_text()) if self.log.exists() else []

    def test_promotes_exact_deployed_digests_to_both_branch_tags(self):
        result = self.run_step()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls(), [
            ["buildx", "imagetools", "create", "--tag", "ghcr.io/kenandkent/sub2api:mysubapi", APP],
            ["buildx", "imagetools", "create", "--tag", "ghcr.io/kenandkent/sub2api-opencode-free-worker:mysubapi", WORKER],
        ])

    def test_invalid_either_digest_fails_before_docker(self):
        cases = [dict(DEPLOY_IMAGE=""), dict(DEPLOY_WORKER_IMAGE=""),
                 dict(DEPLOY_IMAGE="ghcr.io/kenandkent/sub2api:mysubapi"),
                 dict(DEPLOY_WORKER_IMAGE="ghcr.io/kenandkent/sub2api-opencode-free-worker:mysubapi"),
                 dict(DEPLOY_IMAGE="ghcr.io/other/sub2api@sha256:" + "a" * 64),
                 dict(DEPLOY_WORKER_IMAGE="ghcr.io/other/worker@sha256:" + "b" * 64),
                 dict(DEPLOY_IMAGE="ghcr.io/kenandkent/sub2api@sha256:bad"),
                 dict(DEPLOY_WORKER_IMAGE="ghcr.io/kenandkent/sub2api-opencode-free-worker@sha256:bad")]
        for values in cases:
            with self.subTest(values=values):
                result = self.run_step(**values)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.calls(), [])

    def test_first_promotion_failure_stops_before_second(self):
        result = self.run_step(WORKFLOW_FAKE_FAIL="true")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.calls(), [
            ["buildx", "imagetools", "create", "--tag", "ghcr.io/kenandkent/sub2api:mysubapi", APP],
        ])


def promotion_current_step():
    lines = (ROOT / ".github/workflows/mysubapi-ci-cd.yml").read_text().splitlines()
    start = next(i for i, line in enumerate(lines) if "name: Check current commit before promotion" in line)
    start = next(i for i in range(start, len(lines)) if lines[i] == "        run: |") + 1
    end = next((i for i in range(start, len(lines)) if lines[i].strip() and not lines[i].startswith("          ")), len(lines))
    return textwrap.dedent("\n".join(lines[start:end]))


class WorkflowPromotionHeadTests(unittest.TestCase):
    def setUp(self):
        (ROOT / ".cache").mkdir(exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(prefix="promotion-head-test-", dir=ROOT / ".cache")
        self.directory = Path(self.temp.name)
        gh = self.directory / "gh"
        gh.write_text('#!/bin/sh\nprintf "%s\\n" "$FIXTURE_HEAD"\n')
        gh.chmod(0o755)
        self.output = self.directory / "output"
        self.summary = self.directory / "summary"
        self.env = dict(PATH=str(self.directory) + os.pathsep + os.environ["PATH"],
                        GITHUB_REPOSITORY="kenandkent/sub2api", GITHUB_SHA="c" * 40,
                        GITHUB_OUTPUT=str(self.output), GITHUB_STEP_SUMMARY=str(self.summary),
                        GH_TOKEN="fixture-registry-token-sentinel")

    def tearDown(self):
        self.temp.cleanup()

    def test_only_current_head_is_eligible_for_promotion(self):
        for head, expected in [("c" * 40, "promote=true\n"), ("d" * 40, "promote=false\n")]:
            with self.subTest(head=head):
                self.output.unlink(missing_ok=True)
                result = subprocess.run(["bash", "-e", "-c", promotion_current_step()], cwd=ROOT,
                                        env=dict(self.env, FIXTURE_HEAD=head), text=True, capture_output=True, timeout=15)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.output.read_text(), expected)
                self.assertNotIn("fixture-registry-token-sentinel", result.stdout + result.stderr)
