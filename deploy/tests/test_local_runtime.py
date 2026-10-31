"""Local source identity and paired Docker acceptance regressions."""
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("local_runtime", ROOT / "deploy/local_runtime.py")


class LocalRuntimeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.runtime = importlib.util.module_from_spec(SPEC)
        SPEC.loader.exec_module(cls.runtime)

    def setUp(self):
        (ROOT / ".cache").mkdir(exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(prefix="runtime-test-", dir=ROOT / ".cache")
        self.root = Path(self.temp.name)
        (self.root / "backend/cmd/server").mkdir(parents=True)
        (self.root / "backend/cmd/server/VERSION").write_text("1.2.3\n")
        (self.root / ".gitignore").write_text(".env\n.cache/\n")
        self.git("init", "-q")
        self.git("add", ".")
        self.git("-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")

    def tearDown(self):
        self.temp.cleanup()

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.root), *args], text=True)

    def test_source_identity_changes_for_dirty_untracked_deleted_and_mode_inputs(self):
        initial = self.runtime.source_identity(self.root)
        self.assertEqual(initial["dirty"], "false")
        self.assertEqual(initial["version"], "1.2.3")
        (self.root / ".env").write_text("SECRET=do-not-fingerprint\n")
        self.assertEqual(initial, self.runtime.source_identity(self.root))
        source = self.root / "new.py"
        source.write_text("print(1)\n")
        added = self.runtime.source_identity(self.root)
        self.assertEqual(added["dirty"], "true")
        self.assertIn("-dirty.", added["version"])
        self.assertEqual(initial["commit"], added["commit"])
        self.assertNotEqual(initial["digest"], added["digest"])
        source.chmod(0o755)
        executable = self.runtime.source_identity(self.root)
        self.assertNotEqual(added["digest"], executable["digest"])
        (self.root / "backend/cmd/server/VERSION").unlink()
        with self.assertRaisesRegex(ValueError, "VERSION"):
            self.runtime.source_identity(self.root)

    def test_identity_hashes_symlink_itself_without_reading_outside_root(self):
        (self.root / "link").symlink_to("/outside-project-secret")
        identity = self.runtime.source_identity(self.root)
        self.assertEqual(identity["dirty"], "true")
        self.assertRegex(identity["digest"], r"^sha256:[a-f0-9]{64}$")

    def test_dirty_identity_detects_assume_unchanged_and_staged_deletion(self):
        self.git("update-index", "--assume-unchanged", "backend/cmd/server/VERSION")
        (self.root / "backend/cmd/server/VERSION").write_text("2.0.0\n")
        self.assertEqual(self.runtime.source_identity(self.root)["dirty"], "true")
        self.git("update-index", "--no-assume-unchanged", "backend/cmd/server/VERSION")
        self.git("rm", "--cached", "-q", ".gitignore")
        (self.root / ".gitignore").unlink()
        self.assertEqual(self.runtime.source_identity(self.root)["dirty"], "true")

    def report(self):
        identity = self.runtime.source_identity(self.root)
        labels = self.runtime.identity_labels(identity)
        containers = {}
        for name, cid in (("sub2api", "a" * 64), ("opencode-free-worker", "b" * 64)):
            containers[name] = {"id": cid, "image_id": "sha256:" + name,
                                "image": name + ":local", "running": True,
                                "health": "healthy", "labels": dict(labels),
                                "repo_digests": [], "image_labels": dict(labels),
                                "network_mode": "default" if name == "sub2api" else "container:" + "a" * 64}
        return {"source": identity, "containers": containers,
                "app_binary": {"version": identity["version"], "commit": identity["commit"]}}

    def test_check_rejects_missing_identity_stale_worker_network_health_or_binary(self):
        self.assertEqual(self.runtime.validation_errors(self.report()), [])
        for mutate, expected in (
            (lambda r: r["containers"]["sub2api"]["labels"].clear(), "revision"),
            (lambda r: r["containers"]["opencode-free-worker"]["image_labels"].update({"io.sub2api.source.digest": "old"}), "digest"),
            (lambda r: r["containers"]["opencode-free-worker"].update(network_mode="container:old"), "network"),
            (lambda r: r["containers"]["sub2api"].update(health="unhealthy"), "health"),
            (lambda r: r["app_binary"].update(commit="old"), "binary"),
        ):
            with self.subTest(expected=expected):
                report = self.report()
                mutate(report)
                self.assertIn(expected, " ".join(self.runtime.validation_errors(report)))

    def test_inspect_queries_only_allowlisted_fields_and_report_does_not_mutate(self):
        runtime = self.runtime.Runtime(self.root)
        seen = []
        def run(command, **kwargs):
            seen.append(command)
            if "ps" in command:
                return ""
            self.fail("Unexpected command: " + repr(command))
        with mock.patch.object(runtime, "assert_local_docker"), \
             mock.patch.object(runtime, "run", side_effect=run):
            report = runtime.report()
        self.assertTrue(report["errors"])
        self.assertTrue(all("ps" in c for c in seen))
        template = self.runtime.CONTAINER_FORMAT
        self.assertNotIn(".Config.Env", template)
        self.assertNotIn(".State.Health.Log", template)

    def test_rebuild_builds_both_then_forces_recreation_without_deleting_volumes(self):
        runtime = self.runtime.Runtime(self.root)
        seen = []
        with mock.patch.object(runtime, "assert_local_docker"), \
             mock.patch.object(runtime, "assert_ownership"), \
             mock.patch.object(runtime, "verify_built_images"), \
             mock.patch.object(runtime, "run", side_effect=lambda command, **kw: seen.append(command) or ""), \
             mock.patch.object(runtime, "report", return_value={"errors": []}):
            runtime.rebuild(timeout=2)
        build, up = seen
        self.assertIn("build", build)
        self.assertEqual(build[-2:], ["sub2api", "opencode-free-worker"])
        self.assertTrue(any(a.startswith("SOURCE_DIGEST=sha256:") for a in build))
        self.assertIn("--force-recreate", up)
        self.assertIn("--no-build", up)
        self.assertIn("--pull", up)
        self.assertEqual(up[-2:], ["sub2api", "opencode-free-worker"])
        self.assertNotIn("down", up)
        self.assertNotIn("-v", up)

    def test_rebuild_rejects_changed_source_before_restarting_services(self):
        runtime = self.runtime.Runtime(self.root)
        seen = []
        def build(command, **kwargs):
            seen.append(command)
            (self.root / "changed-during-build").write_text("changed")
            return ""
        with mock.patch.object(runtime, "assert_local_docker"), \
             mock.patch.object(runtime, "assert_ownership"), \
             mock.patch.object(runtime, "run", side_effect=build):
            with self.assertRaisesRegex(ValueError, "changed"):
                runtime.rebuild(timeout=2)
        self.assertEqual(len(seen), 1)

    def test_rebuild_refuses_remote_daemon(self):
        runtime = self.runtime.Runtime(self.root)
        with mock.patch.dict(runtime.env, {"DOCKER_HOST": "ssh://production", "DOCKER_CONTEXT": ""}):
            with self.assertRaisesRegex(ValueError, "local Docker"):
                runtime.assert_local_docker()

    def test_built_image_mismatch_aborts_before_runtime_recreation(self):
        runtime = self.runtime.Runtime(self.root)
        seen = []
        def run(command, **kwargs):
            seen.append(command)
            return json.dumps({"image_labels": {}}) if "inspect" in command else ""
        with mock.patch.object(runtime, "assert_local_docker"), \
             mock.patch.object(runtime, "assert_ownership"), \
             mock.patch.object(runtime, "run", side_effect=run):
            with self.assertRaisesRegex(ValueError, "Built image"):
                runtime.rebuild(timeout=1)
        self.assertFalse(any("up" in command for command in seen))

    def test_ownership_refuses_unrelated_existing_app(self):
        runtime = self.runtime.Runtime(self.root)
        with mock.patch.object(runtime, "run", return_value="app-id"), \
             mock.patch.object(runtime, "inspect_container", return_value={"labels": {
                 "com.docker.compose.project": "production",
                 "com.docker.compose.project.working_dir": "/other/checkout/deploy"}}):
            with self.assertRaisesRegex(ValueError, "owned"):
                runtime.assert_ownership()

    def test_compose_uses_existing_named_volume_stack_and_explicit_private_env(self):
        runtime = self.runtime.Runtime(self.root)
        self.assertIn(str(self.root / "deploy/docker-compose.yml"), runtime.compose)
        self.assertNotIn(str(self.root / "deploy/docker-compose.local.yml"), runtime.compose)
        self.assertIn("--env-file", runtime.compose)
        self.assertIn(str(self.root / "deploy/.env"), runtime.compose)

    def test_rebuild_refuses_changed_app_data_mount_even_with_correct_ownership(self):
        runtime = self.runtime.Runtime(self.root)
        app = {"labels": {"com.docker.compose.project": "deploy",
                          "com.docker.compose.project.working_dir": str(self.root / "deploy")},
               "mounts": [{"Type": "bind", "Source": str(self.root / "deploy/data"),
                           "Destination": "/app/data", "RW": True}]}
        with mock.patch.object(runtime, "run", return_value="app-id"), \
             mock.patch.object(runtime, "inspect_container", return_value=app):
            with self.assertRaisesRegex(ValueError, "mount"):
                runtime.assert_ownership()

    def test_report_refuses_remote_context_before_container_inspection(self):
        runtime = self.runtime.Runtime(self.root)
        with mock.patch.object(runtime, "assert_local_docker", side_effect=ValueError("local Docker required")), \
             mock.patch.object(runtime, "run") as run:
            with self.assertRaisesRegex(ValueError, "local Docker"):
                runtime.report()
        run.assert_not_called()

    def test_ambient_application_and_compose_settings_cannot_override_private_env(self):
        with mock.patch.dict(os.environ, {"POSTGRES_PASSWORD": "foreign-password",
                                         "COMPOSE_REMOVE_ORPHANS": "true",
                                         "BUILDX_BUILDER": "foreign-builder"}):
            runtime = self.runtime.Runtime(self.root)
        self.assertFalse("POSTGRES_PASSWORD" in runtime.env)
        self.assertFalse("COMPOSE_REMOVE_ORPHANS" in runtime.env)
        self.assertFalse("BUILDX_BUILDER" in runtime.env)

    def test_missing_running_image_metadata_remains_unverified_without_tag_fallback(self):
        runtime = self.runtime.Runtime(self.root)
        container = self.report()["containers"]["sub2api"]
        seen = []
        def run(command, **kwargs):
            seen.append(command)
            if command[:3] == ["docker", "image", "inspect"]:
                raise ValueError("metadata unavailable")
            return json.dumps(container)
        with mock.patch.object(runtime, "run", side_effect=run):
            actual = runtime.inspect_container("container-id")
        self.assertEqual(actual["image_id"], container["image_id"])
        self.assertEqual(actual["image_labels"], {})
        self.assertFalse(actual["image_metadata_available"])
        self.assertEqual(seen[-1][-1], container["image_id"])
        self.assertNotEqual(seen[-1][-1], container["image"])

    def test_ownership_preflight_does_not_require_old_running_image_metadata(self):
        runtime = self.runtime.Runtime(self.root)
        data = (("deploy_sub2api_data", "/app/data"),
                ("deploy_postgres_data", "/var/lib/postgresql/data"),
                ("deploy_redis_data", "/data"))
        inspections = []
        def inspect(cid, include_image=True):
            inspections.append(include_image)
            volume, destination = data[len(inspections) - 1]
            return {"labels": {"com.docker.compose.project": "deploy",
                               "com.docker.compose.project.working_dir": str(self.root / "deploy")},
                    "mounts": [{"Type": "volume", "Name": volume,
                                "Destination": destination, "RW": True}]}
        with mock.patch.object(runtime, "run", return_value="container-id"), \
             mock.patch.object(runtime, "inspect_container", side_effect=inspect):
            runtime.assert_ownership()
        self.assertEqual(inspections, [False, False, False])

    def test_safe_command_failure_identifies_build_stage_without_rendering_output(self):
        runtime = self.runtime.Runtime(self.root)
        completed = subprocess.CompletedProcess([], 1, stdout="private-build-output", stderr="private-env")
        with mock.patch.object(self.runtime.subprocess, "run", return_value=completed):
            with self.assertRaisesRegex(ValueError, "build") as caught:
                runtime.run([*runtime.compose, "build", *self.runtime.SERVICES])
        self.assertNotIn("private", str(caught.exception))

    def test_cli_displays_only_known_safe_error_categories(self):
        for error, expected, private in (
            (self.runtime.BaselineError("Source changed during build"), "Source changed during build", None),
            (ValueError("private-environment-value"), "configuration", "private-environment-value"),
        ):
            with self.subTest(kind=type(error).__name__):
                output = io.StringIO()
                with mock.patch.object(self.runtime, "Runtime") as runtime, \
                     mock.patch.object(self.runtime.sys, "stderr", output):
                    runtime.return_value.report.side_effect = error
                    self.assertEqual(self.runtime.main(["report"]), 1)
                self.assertIn(expected, output.getvalue())
                if private:
                    self.assertNotIn(private, output.getvalue())


if __name__ == "__main__":
    unittest.main()
