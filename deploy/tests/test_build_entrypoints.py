"""Supported build/test entrypoints must share the backend's working directory."""
from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[2]


class BuildEntrypointTests(unittest.TestCase):
    def dry_run(self, *args):
        result = subprocess.run(["make", "--no-print-directory", "-n", *args,
                                 "VERSION=entrypoint-test"], cwd=ROOT,
                                text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout

    def test_full_image_legacy_path_is_canonical(self):
        self.assertFalse((ROOT / "deploy/Dockerfile").exists())
        self.assertTrue((ROOT / "Dockerfile").is_file())
        for name in ("docker-compose.yml", "docker-compose.local.yml",
                     "docker-compose.standalone.yml", "docker-compose.dev.yml"):
            text = (ROOT / "deploy" / name).read_text()
            self.assertIn("context: ..", text)
            self.assertIn("dockerfile: Dockerfile", text)

    def test_backend_only_image_matches_module_toolchain(self):
        version = re.search(r"^go (\S+)$", (ROOT / "backend/go.mod").read_text(), re.M)[1]
        self.assertIn("FROM golang:" + version + "-alpine", (ROOT / "backend/Dockerfile").read_text())

    def test_legacy_build_and_coverage_targets_delegate_from_both_invocations(self):
        for invocation in (("-C", "deploy"), ("-f", "deploy/Makefile")):
            for target in ("build", "build-embed", "wire", "test-unit", "test-integration",
                           "test-e2e", "test-cover-integration", "clean-coverage", "clean"):
                with self.subTest(invocation=invocation, target=target):
                    output = self.dry_run(*invocation, target)
                    self.assertIn(str(ROOT / "backend"), output)
                    self.assertNotIn("No such file", output)
                    if target.startswith("build"):
                        self.assertIn("main.Version=entrypoint-test", output)

    def test_e2e_targets_use_existing_go_tests(self):
        for target in ("test-e2e", "test-e2e-local"):
            output = self.dry_run("-C", "backend", target)
            self.assertIn("go test -tags=e2e", output)
            self.assertIn("./internal/integration/...", output)
            self.assertIn("-count=1", output)
            self.assertNotIn("e2e-test.sh", output)


if __name__ == "__main__":
    unittest.main()
