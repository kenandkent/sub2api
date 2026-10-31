"""Read-only deployment verification against actual Docker container identities."""
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from contextlib import redirect_stdout, redirect_stderr
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("verify_deployment", ROOT / "deploy/verify-deployment.py")
verify = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(verify)
SHA = "a" * 40
RELEASE = SHA + "-12-1"
APP = "ghcr.io/kenandkent/sub2api@sha256:" + "1" * 64
WORKER = "ghcr.io/kenandkent/sub2api-opencode-free-worker@sha256:" + "2" * 64
APP_ID = "sha256:" + "3" * 64
WORKER_ID = "sha256:" + "4" * 64
SOURCE = "sha256:" + "5" * 64


class IdentityTests(unittest.TestCase):
    def setUp(self):
        (ROOT / ".cache").mkdir(exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(prefix="identity-test-", dir=ROOT / ".cache")
        self.root = Path(self.temp.name)
        (self.root / "current-deployment").write_text(f"{RELEASE} {APP} {WORKER}\n")
        self.running = {"sub2api": [APP_ID, True], "opencode-free-worker": [WORKER_ID, True]}
        self.images = {APP: [APP_ID, SHA, "false", SOURCE], WORKER: [WORKER_ID, SHA, "false", SOURCE]}
        self.calls = []

    def tearDown(self):
        self.temp.cleanup()

    def docker(self, args):
        self.calls.append(args)
        if args == ["context", "show"]:
            return "default"
        if args[0] == "ps":
            service = args[-1].split("=", 2)[-1]
            if service not in self.running:
                return ""
            return service + "-container"
        if args[:2] == ["container", "inspect"]:
            return json.dumps(self.running[args[-1].removesuffix("-container")])
        if args[:2] == ["image", "inspect"]:
            return json.dumps(self.images[args[-1]])
        self.fail(f"unexpected Docker command: {args}")

    def run_verify(self, *args):
        out, err = io.StringIO(), io.StringIO()
        with patch.object(verify, "docker", self.docker), patch.dict(os.environ, {"DOCKER_HOST": "", "DOCKER_CONTEXT": "default"}), redirect_stdout(out), redirect_stderr(err):
            code = verify.main([str(self.root), *args])
        return code, out.getvalue() + err.getvalue()

    def test_current_state_checks_actual_container_ids_and_paired_revisions(self):
        code, output = self.run_verify()
        self.assertEqual(code, 0, output)
        self.assertIn("Deployment identity verified", output)
        self.assertTrue(any(c[:2] == ["container", "inspect"] for c in self.calls))
        self.assertTrue(any(c[:2] == ["image", "inspect"] and c[-1] == APP for c in self.calls))
        self.assertTrue(all(c[0] in {"context", "ps", "container", "image"} for c in self.calls))

    def test_wrong_running_image_fails_even_when_revision_matches(self):
        self.running["sub2api"][0] = "sha256:" + "9" * 64
        code, output = self.run_verify()
        self.assertEqual(code, 1)
        self.assertIn("sub2api", output)
        self.assertIn("image ID mismatch", output)

    def test_wrong_or_missing_revision_and_dirty_source_fail(self):
        for revision, dirty in [("b" * 40, "false"), (None, None), (SHA, "true"), (SHA, "unexpected")]:
            with self.subTest(revision=revision, dirty=dirty):
                self.images[WORKER] = [WORKER_ID, revision, dirty, SOURCE]
                code, output = self.run_verify()
                self.assertEqual(code, 1, output)
                self.assertIn("opencode-free-worker", output)

    def test_different_worker_source_digest_fails(self):
        self.images[WORKER][3] = "sha256:" + "6" * 64
        code, output = self.run_verify()
        self.assertEqual(code, 1)
        self.assertIn("source digest", output)

    def test_two_field_legacy_state_and_legacy_labels_remain_supported(self):
        (self.root / "current-deployment").write_text(f"{RELEASE} {APP}\n")
        del self.running["opencode-free-worker"]
        self.images[APP] = [APP_ID, SHA, None, None]
        code, output = self.run_verify()
        self.assertEqual(code, 0, output)
        self.assertIn("legacy", output)

    def test_legacy_pair_without_custom_source_labels_is_supported(self):
        self.images[APP][2:] = [None, None]
        self.images[WORKER][2:] = [None, None]
        code, output = self.run_verify()
        self.assertEqual(code, 0, output)

    def test_disabled_worker_rejects_unexpected_running_worker(self):
        (self.root / "current-deployment").write_text(f"{RELEASE} {APP} -\n")
        code, output = self.run_verify()
        self.assertEqual(code, 1)
        self.assertIn("unexpected", output)

    def test_stopped_or_missing_app_fails(self):
        self.running["sub2api"][1] = False
        self.assertEqual(self.run_verify()[0], 1)
        del self.running["sub2api"]
        self.assertEqual(self.run_verify()[0], 1)

    def test_explicit_pending_release_does_not_require_current_state_update(self):
        (self.root / "current-deployment").unlink()
        code, output = self.run_verify("--release", RELEASE, "--image", APP, "--worker", WORKER)
        self.assertEqual(code, 0, output)
        self.assertFalse((self.root / "current-deployment").exists())

    def test_invalid_state_rejected_before_docker(self):
        for content in [f"{RELEASE} {APP} - extra\n", f"{RELEASE} {APP}\n\n", f"{RELEASE} ghcr.io/kenandkent/sub2api:latest\n"]:
            (self.root / "current-deployment").write_text(content)
            self.calls.clear()
            self.assertEqual(self.run_verify()[0], 1)
            self.assertEqual(self.calls, [])

    def test_docker_failure_does_not_echo_sensitive_stderr(self):
        import subprocess
        with patch.object(verify.subprocess, "run", side_effect=subprocess.CalledProcessError(1, ["docker"], stderr="private-env-sentinel")):
            out, err = io.StringIO(), io.StringIO()
            with redirect_stdout(out), redirect_stderr(err):
                self.assertEqual(verify.main([str(self.root)]), 1)
            self.assertNotIn("private-env-sentinel", out.getvalue() + err.getvalue())


if __name__ == "__main__":
    unittest.main()
