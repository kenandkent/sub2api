#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

# Keep every generated fixture and subprocess temporary file inside the project.
mkdir -p "$repo_root/.cache"
TMPDIR="$repo_root/.cache"
export TMPDIR

python3 - "$repo_root" <<'PY'
import copy
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile


repo_root = Path(sys.argv[1])
image = "ghcr.io/kenandkent/sub2api@sha256:" + "a" * 64


def seconds(value):
    parts = re.findall(r"(\d+(?:\.\d+)?)(h|m|s)", value)
    assert "".join(number + unit for number, unit in parts) == value, value
    return sum(float(number) * {"h": 3600, "m": 60, "s": 1}[unit]
               for number, unit in parts)


with tempfile.TemporaryDirectory(prefix="compose-production-", dir=repo_root / ".cache") as fixture:
    fixture = Path(fixture)
    temporary_files = fixture / "tmp"
    temporary_files.mkdir()
    # Use controlled values and disable automatic .env discovery. The test must
    # never read the local deployment's private environment or contact Docker.
    env = {
        "PATH": os.environ["PATH"],
        "HOME": os.environ["HOME"],
        "TMPDIR": str(temporary_files),
        "POSTGRES_USER": "fixture_user",
        "POSTGRES_DB": "fixture_database",
        "POSTGRES_PASSWORD": "compose-fixture-password",
        "REDIS_PASSWORD": "compose-fixture-redis-password",
        "BIND_HOST": "127.0.0.1",
        "SERVER_PORT": "18080",
        "SUB2API_IMAGE": image,
    }
    version = subprocess.run(
        ["docker", "compose", "version", "--short"],
        env=env, check=True, capture_output=True, text=True,
    ).stdout.strip()
    match = re.match(r"v?(\d+)\.(\d+)\.(\d+)", version)
    assert match and tuple(map(int, match.groups())) >= (2, 24, 4), \
        f"Docker Compose >= 2.24.4 is required for !reset (found {version})"

    def render(project_directory, files, environment=env, check=True):
        command = [
            "docker", "compose", "--env-file", "/dev/null",
            "--project-name", "sub2api-compose-test",
            "--project-directory", str(project_directory),
        ]
        for path in files:
            command.extend(["-f", str(path)])
        command.extend(["config", "--format", "json"])
        return subprocess.run(
            command, env=environment, check=check, capture_output=True, text=True,
        )

    local_file = repo_root / "deploy/docker-compose.local.yml"
    local = json.loads(render(repo_root / "deploy", [local_file]).stdout)
    services = local["services"]
    assert set(services) == {"sub2api", "postgres", "redis"}, \
        "local deployment must retain exactly the application, PostgreSQL and Redis"
    app = services["sub2api"]
    assert app["image"] == "sub2api:mysubapi", "local image name changed"
    assert app["pull_policy"] == "build", "local deployment must build current source"
    assert app["build"]["context"] == str(repo_root), "local build context must be the repository"
    assert app["build"]["dockerfile"] == "Dockerfile", "local deployment must use the root Dockerfile"
    assert seconds(app["healthcheck"]["start_period"]) == 120, \
        "application bootstrap must have a 120-second health grace period"
    assert seconds(app["stop_grace_period"]) == 60, \
        "application must receive 60 seconds to stop gracefully"
    postgres_health = services["postgres"]["healthcheck"]
    assert postgres_health["test"] == [
        "CMD-SHELL",
        "pg_isready -U fixture_user -d fixture_database && "
        "psql -U fixture_user -d fixture_database -Atqc 'SELECT 1' >/dev/null",
    ], "PostgreSQL readiness must verify that queries succeed"
    assert postgres_health["retries"] == 18, "PostgreSQL bootstrap needs 18 readiness attempts"
    assert seconds(postgres_health["start_period"]) == 60, \
        "PostgreSQL bootstrap must have a 60-second health grace period"
    assert app["environment"]["DATABASE_PASSWORD"] == "compose-fixture-password"
    assert app["ports"][0]["host_ip"] == "127.0.0.1"
    assert app["ports"][0]["published"] == "18080"
    print("PASS local Compose keeps source builds and bootstrap/shutdown grace periods")

    deployment_root = fixture / "root"
    release = deployment_root / "releases" / ("a" * 40)
    release.mkdir(parents=True)
    for filename in ("docker-compose.local.yml", "docker-compose.production.yml"):
        shutil.copyfile(repo_root / "deploy" / filename, release / filename)
    # Intentionally provide conflicting fixture .env files. Explicit env-file
    # selection must keep both of them out of the rendered configuration.
    for directory in (deployment_root, release):
        (directory / ".env").write_text("SERVER_PORT=19999\nSUB2API_IMAGE=wrong-image\n")
    assert not (deployment_root / "Dockerfile").exists()
    files = [release / "docker-compose.local.yml", release / "docker-compose.production.yml"]
    production = json.loads(render(deployment_root, files).stdout)
    assert set(production["services"]) == {"sub2api", "postgres", "redis"}, \
        "production override must retain all three services"
    production_app = production["services"]["sub2api"]
    assert "build" not in production_app, "production must not require source code or a build context"
    assert production_app["image"] == image, "production must preserve the exact supplied image digest"
    assert production_app["pull_policy"] == "missing", "production must pull missing release images"

    for name, service in production["services"].items():
        expected = copy.deepcopy(services[name])
        for mount in expected["volumes"]:
            mount["source"] = str(deployment_root / Path(mount["source"]).name)
        if name == "sub2api":
            expected.pop("build")
            expected["image"] = image
            expected["pull_policy"] = "missing"
        assert service == expected, \
            f"production override unexpectedly changes {name} settings or persistent data paths"
    for name, directory, target in (
        ("sub2api", "data", "/app/data"),
        ("postgres", "postgres_data", "/var/lib/postgresql/data"),
        ("redis", "redis_data", "/data"),
    ):
        mount = production["services"][name]["volumes"][0]
        assert mount["source"] == str(deployment_root / directory), \
            f"{name} data must resolve at deployment root, outside releases/<sha>"
        assert mount["target"] == target
        assert not (deployment_root / directory).exists(), "config validation must not create data directories"
    print("PASS production Compose uses the pinned image without build and preserves root data paths")

    no_image = dict(env)
    no_image.pop("SUB2API_IMAGE")
    missing = render(deployment_root, files, environment=no_image, check=False)
    assert missing.returncode != 0, "production must reject a missing SUB2API_IMAGE"
    assert "SUB2API_IMAGE is required" in missing.stderr, \
        "production failure must identify the required image variable"
    print("PASS production Compose rejects a missing SUB2API_IMAGE")

print("docker compose production test passed")
PY
