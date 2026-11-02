"""Render the full local stack's Redis image without reading private config."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


class RedisImageTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Keep fixtures and subprocess temporary files inside the project even
        # when the caller's default TMPDIR points outside the checkout.
        cache = Path(os.environ.get("TMPDIR", ROOT / ".cache")).resolve()
        if not cache.is_relative_to(ROOT):
            cache = ROOT / ".cache"
        cache.mkdir(parents=True, exist_ok=True)
        cls.temp = tempfile.TemporaryDirectory(prefix="compose-redis-image-", dir=cache)
        cls.addClassCleanup(cls.temp.cleanup)
        cls.fixture = Path(cls.temp.name)
        temporary_files = cls.fixture / "tmp"
        temporary_files.mkdir()
        docker_config = cls.fixture / "docker"
        docker_config.mkdir()
        # Discover Compose without loading or changing the real Docker config.
        original_docker_config = Path(os.environ.get("DOCKER_CONFIG", Path.home() / ".docker"))
        (docker_config / "config.json").write_text(json.dumps({
            "cliPluginsExtraDirs": [str(original_docker_config / "cli-plugins")],
        }))
        cls.env = {
            "PATH": os.environ["PATH"],
            "HOME": os.environ["HOME"],
            "TMPDIR": str(temporary_files),
            "DOCKER_CONFIG": str(docker_config),
            "POSTGRES_PASSWORD": "fixture-postgres-password",
        }

    def render_image(self, **overrides):
        # The clean environment and explicit env file exclude deploy/.env and
        # ambient Compose settings. Config rendering never contacts the daemon.
        result = subprocess.run(
            [
                "docker", "compose", "-p", "sub2api-redis-image-test",
                "--project-directory", str(ROOT / "deploy"),
                "--env-file", "/dev/null", "-f", str(ROOT / "deploy/docker-compose.yml"),
                "config", "--format", "json",
            ],
            cwd=self.fixture, env=dict(self.env, **overrides), capture_output=True, text=True,
        )
        # Do not include the full rendered environment in test failure output.
        self.assertEqual(result.returncode, 0, "Compose image rendering failed")
        return json.loads(result.stdout)["services"]["redis"]["image"]

    def test_unset_image_preserves_default(self):
        self.assertEqual(self.render_image(), "redis:8-alpine")

    def test_empty_image_preserves_default(self):
        self.assertEqual(self.render_image(REDIS_IMAGE=""), "redis:8-alpine")

    def test_explicit_version_can_be_rendered(self):
        self.assertEqual(self.render_image(REDIS_IMAGE="redis:8.10.3-alpine"),
                         "redis:8.10.3-alpine")

    def test_explicit_digest_can_be_rendered(self):
        reference = "redis@sha256:" + "a" * 64
        self.assertEqual(self.render_image(REDIS_IMAGE=reference), reference)


if __name__ == "__main__":
    unittest.main()
