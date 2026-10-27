#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

# Keep fixtures and subprocess temporary files inside the project boundary.
mkdir -p "$repo_root/.cache"
TMPDIR="$repo_root/.cache"
export TMPDIR
export PYTHONDONTWRITEBYTECODE=1

python3 - "$repo_root" <<'PY'
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


repo_root = Path(sys.argv[1])
compose_file = repo_root / "deploy/docker-compose.dependencies.yml"


def seconds(value):
    parts = re.findall(r"(\d+(?:\.\d+)?)(h|m|s)", value)
    assert "".join(number + unit for number, unit in parts) == value, value
    return sum(float(number) * {"h": 3600, "m": 60, "s": 1}[unit]
               for number, unit in parts)


with tempfile.TemporaryDirectory(prefix="compose-dependencies-", dir=repo_root / ".cache") as fixture:
    fixture = Path(fixture)
    temporary_files = fixture / "tmp"
    temporary_files.mkdir()
    docker_config = fixture / "docker"
    docker_config.mkdir()
    # Docker Desktop can install Compose under the user's CLI plugin directory.
    # Keep that plugin discoverable without loading or writing the real config.
    original_docker_config = Path(os.environ.get("DOCKER_CONFIG", Path.home() / ".docker"))
    (docker_config / "config.json").write_text(json.dumps({
        "cliPluginsExtraDirs": [str(original_docker_config / "cli-plugins")],
    }))
    # A clean environment and explicit /dev/null prevent discovering the real
    # deploy/.env or .dev/.env. Config parsing never contacts the Docker daemon.
    env = {
        "PATH": os.environ["PATH"],
        "HOME": os.environ["HOME"],
        "TMPDIR": str(temporary_files),
        "DOCKER_CONFIG": str(docker_config),
        "DEV_POSTGRES_PASSWORD": "fixture-postgres-password",
        "DEV_REDIS_PASSWORD": "fixture-redis-password",
    }

    def render(environment):
        return subprocess.run(
            [
                "docker", "compose", "-p", "sub2api-dev",
                "--project-directory", str(repo_root / "deploy"),
                "--env-file", "/dev/null", "-f", str(compose_file),
                "config", "--format", "json",
            ],
            cwd=fixture, env=environment, capture_output=True, text=True,
        )

    def parse(environment):
        result = render(environment)
        assert result.returncode == 0, result.stderr
        return json.loads(result.stdout)

    data_paths = [repo_root / ".dev/postgres_data", repo_root / ".dev/redis_data"]
    existing_paths = {path: path.exists() for path in data_paths}

    def check_common(config, postgres_port, redis_port):
        assert config["name"] == "sub2api-dev", "development project must be isolated"
        services = config["services"]
        assert set(services) == {"postgres", "redis"}, \
            "dependencies Compose must contain exactly PostgreSQL and Redis, without the application"
        assert set(config["networks"]) == {"default"}, "dependencies need a private project network"
        network = config["networks"]["default"]
        assert network["name"] == "sub2api-dev_default"
        assert not network.get("external", False), "do not share the deployment network"
        for name, image, port, target, source, mount_target in (
            ("postgres", "postgres:18-alpine", postgres_port, 5432,
             repo_root / ".dev/postgres_data", "/var/lib/postgresql/data"),
            ("redis", "redis:8-alpine", redis_port, 6379,
             repo_root / ".dev/redis_data", "/data"),
        ):
            service = services[name]
            assert service["image"] == image, f"unexpected {name} image"
            assert "container_name" not in service, f"{name} must use project-scoped container names"
            assert "build" not in service, f"{name} must not build the application"
            assert "network_mode" not in service, f"{name} must stay on its project network"
            assert len(service["ports"]) == 1, f"unexpected extra {name} ports"
            published = service["ports"][0]
            assert published["host_ip"] == "127.0.0.1", f"{name} must bind only to loopback"
            assert published["published"] == str(port), f"wrong {name} host port"
            assert published["target"] == target
            assert len(service["volumes"]) == 1, f"unexpected extra {name} data mounts"
            mount = service["volumes"][0]
            assert mount["type"] == "bind"
            assert mount["source"] == str(source), f"{name} data must be isolated in .dev"
            assert mount["target"] == mount_target

        postgres = services["postgres"]
        assert postgres["environment"]["PGDATA"] == "/var/lib/postgresql/data", \
            "PostgreSQL 18 must use the mounted PGDATA path"
        assert postgres["environment"]["POSTGRES_PASSWORD"] == env["DEV_POSTGRES_PASSWORD"]
        health = postgres["healthcheck"]
        assert health["test"][0] == "CMD-SHELL"
        command = health["test"][1]
        assert "pg_isready" in command and "psql" in command and "SELECT 1" in command, \
            "PostgreSQL health must prove readiness and a successful SQL query"
        assert "&&" in command, "both PostgreSQL checks must succeed"
        assert health["retries"] == 18
        assert seconds(health["start_period"]) == 60

        redis = services["redis"]
        command = redis["command"]
        assert command[0] == "redis-server"
        assert command[command.index("--appendonly") + 1] == "yes", "Redis AOF must be enabled"
        assert command[command.index("--requirepass") + 1] == env["DEV_REDIS_PASSWORD"], \
            "Redis must require the development password"
        assert redis["environment"]["REDISCLI_AUTH"] == env["DEV_REDIS_PASSWORD"]
        health = redis["healthcheck"]["test"]
        assert "redis-cli" in " ".join(health) and "ping" in " ".join(health), \
            "Redis health must query the authenticated service"

    default = parse(env)
    check_common(default, 15433, 16380)
    assert default["services"]["postgres"]["environment"]["POSTGRES_USER"] == "sub2api_dev"
    assert default["services"]["postgres"]["environment"]["POSTGRES_DB"] == "sub2api_dev"
    print("PASS default dependencies have isolated data, loopback ports, authentication and SQL health")

    overrides = dict(env, DEV_POSTGRES_PORT="25433", DEV_REDIS_PORT="26380",
                     DEV_POSTGRES_USER="fixture_user", DEV_POSTGRES_DB="fixture_database")
    custom = parse(overrides)
    check_common(custom, 25433, 26380)
    assert custom["services"]["postgres"]["environment"]["POSTGRES_USER"] == "fixture_user"
    assert custom["services"]["postgres"]["environment"]["POSTGRES_DB"] == "fixture_database"
    print("PASS development ports, PostgreSQL user and database accept explicit overrides")

    for variable in ("DEV_POSTGRES_PASSWORD", "DEV_REDIS_PASSWORD"):
        for value in (None, ""):
            missing = dict(env)
            if value is None:
                missing.pop(variable)
            else:
                missing[variable] = value
            result = render(missing)
            assert result.returncode != 0, f"Compose must reject a missing or empty {variable}"
            assert variable in result.stderr, f"failure must identify {variable}"
        print(f"PASS missing or empty {variable} fails before any container operation")

    assert existing_paths == {path: path.exists() for path in data_paths}, \
        "config validation must not create development data directories"

print("docker compose dependencies test passed")
PY
