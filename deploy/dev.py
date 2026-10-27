#!/usr/bin/env python3
"""Native Air/Vite development with project-local tools and Docker dependencies."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
AIR_VERSION = "v1.67.4"
PNPM_VERSION = "9.15.9"
CANCEL = threading.Event()


def write_private_once(path, text):
    try:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        return
    with os.fdopen(descriptor, "w") as stream:
        stream.write(text)


def read_settings(path):
    result = {}
    for number, line in enumerate(path.read_text().splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        if not separator or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", key):
            raise ValueError("Invalid development environment line " + str(number))
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        result[key] = value
    return result


def initialize(root):
    directory = root / ".dev"
    directory.mkdir(mode=0o700, exist_ok=True)
    for name in ("backend", "backend/pricing", "logs", "postgres_data", "redis_data"):
        (directory / name).mkdir(mode=0o700, parents=True, exist_ok=True)
    defaults = {
        "DEV_FRONTEND_PORT": "3000", "DEV_BACKEND_PORT": "18080",
        "DEV_POSTGRES_PORT": "15433", "DEV_REDIS_PORT": "16380",
        "DEV_POSTGRES_USER": "sub2api_dev", "DEV_POSTGRES_DB": "sub2api_dev",
        "DEV_POSTGRES_PASSWORD": secrets.token_hex(24),
        "DEV_REDIS_PASSWORD": secrets.token_hex(24),
        "DEV_ADMIN_EMAIL": "admin@mysubapi-dev.local",
        "DEV_ADMIN_PASSWORD": secrets.token_urlsafe(24),
        "DEV_JWT_SECRET": secrets.token_hex(32),
        "DEV_TOTP_ENCRYPTION_KEY": secrets.token_hex(32),
        "DEV_GOPROXY": "https://goproxy.cn,direct",
    }
    write_private_once(directory / ".env",
                       "# Private local development settings; never shared with deployment.\n"
                       + "".join(key + "=" + value + "\n" for key, value in defaults.items()))
    # A separate override file keeps AUTO_SETUP's config.yaml absence check valid.
    write_private_once(directory / "backend/development.yaml",
                       "# Local fallback pricing keeps native restarts independent of network access.\n"
                       "pricing:\n  remote_url: \"\"\n  hash_url: \"\"\n")
    return read_settings(directory / ".env")


def development_env(root, settings):
    ports = {}
    for key in ("DEV_FRONTEND_PORT", "DEV_BACKEND_PORT", "DEV_POSTGRES_PORT", "DEV_REDIS_PORT"):
        value = settings.get(key, "")
        if not value.isdigit() or not 1024 < int(value) < 65536:
            raise ValueError("Invalid " + key + "; use a port between 1025 and 65535")
        ports[key] = value
    if len(set(ports.values())) != 4:
        raise ValueError("The four development ports must be distinct")
    for key in ("DEV_POSTGRES_USER", "DEV_POSTGRES_DB", "DEV_POSTGRES_PASSWORD",
                "DEV_REDIS_PASSWORD", "DEV_ADMIN_EMAIL", "DEV_ADMIN_PASSWORD",
                "DEV_JWT_SECRET", "DEV_TOTP_ENCRYPTION_KEY"):
        if not settings.get(key):
            raise ValueError("Missing " + key + " in .dev/.env")
    version = re.search(r"(?m)^go\s+([0-9.]+)\s*$", (root / "backend/go.mod").read_text()).group(1)
    cache = root / ".cache/dev"
    inherited = {"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "LANG", "NO_COLOR",
                 "SSH_AUTH_SOCK", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
                 "http_proxy", "https_proxy", "all_proxy", "no_proxy",
                 "SSL_CERT_FILE", "SSL_CERT_DIR", "NPM_CONFIG_REGISTRY"}
    env = {key: value for key, value in os.environ.items() if key in inherited or key.startswith("LC_")}
    env.update({key: value for key, value in settings.items() if key.startswith("DEV_")})
    env.update({
        "DATA_DIR": str(root / ".dev/backend"),
        "CONFIG_FILE": str(root / ".dev/backend/development.yaml"),
        "PRICING_DATA_DIR": str(root / ".dev/backend/pricing"),
        "PRICING_FALLBACK_FILE": str(root / "backend/resources/model-pricing/model_prices_and_context_window.json"),
        "LOG_ENV": "development", "LOG_SERVICE_NAME": "sub2api-dev",
        "LOG_OUTPUT_FILE_PATH": str(root / ".dev/backend/logs/sub2api.log"),
        "PLUGINS_DATA_DIR": str(root / ".dev/backend/plugins"),
        "AUTO_SETUP": "true", "SERVER_HOST": "127.0.0.1",
        "SERVER_PORT": ports["DEV_BACKEND_PORT"], "SERVER_MODE": "debug",
        "SERVER_FRONTEND_URL": "http://localhost:" + ports["DEV_FRONTEND_PORT"],
        "DATABASE_HOST": "127.0.0.1", "DATABASE_PORT": ports["DEV_POSTGRES_PORT"],
        "DATABASE_USER": settings["DEV_POSTGRES_USER"], "DATABASE_DBNAME": settings["DEV_POSTGRES_DB"],
        "DATABASE_PASSWORD": settings["DEV_POSTGRES_PASSWORD"], "DATABASE_SSLMODE": "disable",
        "REDIS_HOST": "127.0.0.1", "REDIS_PORT": ports["DEV_REDIS_PORT"],
        "REDIS_USERNAME": "", "REDIS_PASSWORD": settings["DEV_REDIS_PASSWORD"],
        "REDIS_ENABLE_TLS": "false", "REDIS_DB": "0",
        "ADMIN_EMAIL": settings["DEV_ADMIN_EMAIL"], "ADMIN_PASSWORD": settings["DEV_ADMIN_PASSWORD"],
        "JWT_SECRET": settings["DEV_JWT_SECRET"], "TOTP_ENCRYPTION_KEY": settings["DEV_TOTP_ENCRYPTION_KEY"],
        "RUN_MODE": settings.get("DEV_RUN_MODE", "standard"), "TZ": "Asia/Shanghai",
        "GOTOOLCHAIN": "go" + version, "GOENV": "off", "GOWORK": "off", "CGO_ENABLED": "0",
        "GOCACHE": str(cache / "go-build"), "GOMODCACHE": str(cache / "go-mod"),
        "GOPATH": str(cache / "go"), "GOBIN": str(cache / "bin"),
        "GOTMPDIR": str(cache / "tmp"), "TMPDIR": str(cache / "tmp"),
        "GOPROXY": settings.get("DEV_GOPROXY", "https://goproxy.cn,direct"),
        "GOTELEMETRY": "off", "GOTELEMETRYDIR": str(cache / "go-telemetry"),
        "COREPACK_HOME": str(cache / "corepack"), "COREPACK_ENABLE_AUTO_PIN": "0",
        "COREPACK_ENABLE_DOWNLOAD_PROMPT": "0",
        "XDG_CACHE_HOME": str(cache / "cache"), "XDG_DATA_HOME": str(cache / "share"),
        "PNPM_HOME": str(cache / "pnpm-home"), "npm_config_store_dir": str(cache / "pnpm-store"),
        "npm_config_cache": str(cache / "npm-cache"),
        "VITE_API_BASE_URL": "/api/v1", "VITE_DEV_PORT": ports["DEV_FRONTEND_PORT"],
        "VITE_DEV_PROXY_TARGET": "http://127.0.0.1:" + ports["DEV_BACKEND_PORT"],
    })
    for key in ("GOCACHE", "GOMODCACHE", "GOPATH", "GOBIN", "TMPDIR", "COREPACK_HOME",
                "XDG_CACHE_HOME", "XDG_DATA_HOME", "PNPM_HOME", "npm_config_store_dir",
                "npm_config_cache", "GOTELEMETRYDIR"):
        Path(env[key]).mkdir(parents=True, exist_ok=True)
    return env


def retry_run(command, env, cwd, timeout):
    for attempt in range(1, 4):
        try:
            process = subprocess.Popen(command, env=env, cwd=cwd, start_new_session=True)
            deadline = time.monotonic() + timeout
            try:
                while True:
                    if CANCEL.is_set():
                        raise InterruptedError("Development setup cancelled")
                    if time.monotonic() >= deadline:
                        raise subprocess.TimeoutExpired(command, timeout)
                    try:
                        result = process.wait(timeout=0.25)
                    except subprocess.TimeoutExpired:
                        continue
                    if result:
                        raise subprocess.CalledProcessError(result, command)
                    break
            except BaseException:
                stop_children([process], timeout=2)
                raise
            return
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
            if attempt == 3:
                raise
            print("Command failed; retrying (%d/3)." % attempt, flush=True)
            time.sleep(2)


def setup(root, env):
    for program in ("go", "node", "corepack", "docker"):
        if not shutil.which(program):
            raise ValueError("Required program is unavailable: " + program)
    major = int(subprocess.check_output(["node", "--version"], env=env, text=True).strip().lstrip("v").split(".")[0])
    if major < 20:
        raise ValueError("Node.js 20 or newer is required; Node.js 24 is recommended")

    def air():
        marker = root / ".cache/dev/air-version"
        if (root / ".cache/dev/bin/air").is_file() and marker.exists() and marker.read_text() == AIR_VERSION:
            return
        print("Preparing " + env["GOTOOLCHAIN"] + " and Air " + AIR_VERSION + " in .cache/dev.", flush=True)
        retry_run(["go", "install", "github.com/air-verse/air@" + AIR_VERSION], env, root / "backend", 600)
        marker.write_text(AIR_VERSION)

    def frontend():
        digest = hashlib.sha256()
        for name in ("package.json", "pnpm-lock.yaml"):
            digest.update((root / "frontend" / name).read_bytes())
        digest.update(PNPM_VERSION.encode())
        marker = root / ".cache/dev/frontend-dependencies"
        if (root / "frontend/node_modules/.modules.yaml").exists() and marker.exists() and marker.read_text() == digest.hexdigest():
            return
        print("Preparing frontend dependencies with project-local pnpm " + PNPM_VERSION + ".", flush=True)
        retry_run(["corepack", "pnpm@" + PNPM_VERSION, "--dir", str(root / "frontend"),
                   "install", "--frozen-lockfile", "--store-dir", env["npm_config_store_dir"]],
                  env, root, 600)
        marker.write_text(digest.hexdigest())

    with ThreadPoolExecutor(max_workers=2) as workers:
        futures = [workers.submit(air), workers.submit(frontend)]
        for future in futures:
            future.result()


def compose(root):
    return ["docker", "compose", "--progress", "plain", "--project-name", "sub2api-dev",
            "--project-directory", str(root / "deploy"), "--env-file", str(root / ".dev/.env"),
            "-f", str(root / "deploy/docker-compose.dependencies.yml")]


def process_start(pid):
    result = subprocess.run(["ps", "-p", str(pid), "-o", "lstart=", "-o", "stat="],
                            env=dict(os.environ, LC_ALL="C"), text=True, capture_output=True)
    if result.returncode or not result.stdout.strip():
        return ""
    started, status = result.stdout.strip().rsplit(None, 1)
    return "" if status.startswith("Z") else started


def atomic_state(path, state):
    descriptor, filename = tempfile.mkstemp(prefix="." + path.name, suffix=".tmp", dir=path.parent)
    temporary = Path(filename)
    try:
        with os.fdopen(descriptor, "w") as stream:
            json.dump(state, stream)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def read_state(path):
    try:
        state = json.loads(path.read_text())
    except FileNotFoundError:
        return {}
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError("Invalid state file " + str(path) + "; inspect its processes and lock before recovery") from error
    if not isinstance(state, dict):
        raise ValueError("Invalid state object in " + str(path))
    return state


def is_our_supervisor(state, root):
    if state.get("root") != str(root) or not state.get("started") or process_start(state.get("pid", -1)) != state["started"]:
        return False
    command = subprocess.run(["ps", "-p", str(state["pid"]), "-o", "command="], text=True, capture_output=True).stdout
    return str(root / "deploy/dev.py") in command and "up" in command


def active_groups():
    output = subprocess.check_output(["ps", "-A", "-o", "pgid=", "-o", "stat="], text=True)
    return {int(parts[0]) for line in output.splitlines()
            if len(parts := line.split()) == 2 and not parts[1].startswith("Z")}


def stop_children(children, timeout=8):
    for process in children:
        try:
            # A group can outlive its leader (for example Corepack -> Vite).
            os.killpg(process.pid, signal.SIGINT)
        except ProcessLookupError:
            pass
    deadline = time.monotonic() + timeout
    groups = {process.pid for process in children}
    while groups and time.monotonic() < deadline:
        groups.intersection_update(active_groups())
        if groups:
            time.sleep(0.05)
    for group in groups:
        try:
            os.killpg(group, signal.SIGKILL)
        except ProcessLookupError:
            pass
    for process in children:
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait()


def stop_record(record, root):
    pid, started = record.get("pid"), record.get("started")
    if not pid or not started:
        return False
    current = process_start(pid)
    if current != started:
        if not current and pid in active_groups():
            raise ValueError("Orphaned process group " + str(pid) + " needs ownership inspection; state retained")
        return False
    command = subprocess.run(["ps", "-p", str(pid), "-o", "command="], text=True, capture_output=True).stdout
    if str(root) not in command:
        return False
    grouped = os.getpgid(pid) == pid
    try:
        (os.killpg if grouped else os.kill)(pid, signal.SIGINT)
        deadline = time.monotonic() + 6
        def alive():
            return pid in active_groups() if grouped else process_start(pid) == started
        while alive() and time.monotonic() < deadline:
            time.sleep(0.05)
        if alive():
            (os.killpg if grouped else os.kill)(pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    return True


def stop_backend(root, owner):
    path = root / ".dev/backend-process.json"
    record = read_state(path)
    if record.get("root") != str(root) or record.get("owner") != owner:
        return
    stop_record(record, root)
    path.unlink(missing_ok=True)


def backend_exec(root):
    if not Path(sys.argv[0]).is_absolute():
        os.execv(sys.executable, [sys.executable, "-B", str(root / "deploy/dev.py"), "backend-exec"])
    state = read_state(root / ".dev/session.json")
    owner = os.environ.get("SUB2API_DEV_SESSION")
    if not owner or state.get("session") != owner or not is_our_supervisor(state, root):
        raise ValueError("Backend entrypoint requires an owned make dev session")
    atomic_state(root / ".dev/backend-process.json",
                 {"root": str(root), "pid": os.getpid(), "started": process_start(os.getpid()), "owner": owner})
    binary = str(root / ".cache/dev/bin/server")
    os.execve(binary, [binary], dict(os.environ))


def available(port):
    with socket.socket() as listener:
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            listener.bind(("127.0.0.1", int(port)))
        except OSError:
            return False
    return True


def up(root, settings, env):
    lock = root / ".dev/session.lock"
    try:
        lock.mkdir()
    except FileExistsError:
        raise ValueError("Development session already exists; use make dev-status or make dev-stop")
    children, logs = [], []
    state_file = root / ".dev/session.json"
    try:
        state = {"root": str(root), "pid": os.getpid(), "started": process_start(os.getpid()),
                 "session": secrets.token_hex(16), "children": []}
        atomic_state(state_file, state)
        for key in ("DEV_FRONTEND_PORT", "DEV_BACKEND_PORT"):
            if not available(settings[key]):
                raise ValueError(key + " is occupied; adjust .dev/.env without stopping unrelated services")
        setup(root, env)
        retry_run(compose(root) + ["up", "-d", "--wait", "--wait-timeout", "120"], env, root, 180)
        env = dict(env, SUB2API_DEV_SESSION=state["session"])
        commands = [
            ("backend", [str(root / ".cache/dev/bin/air"), "-c", str(root / "backend/.air.toml")], root / "backend"),
            ("frontend", ["corepack", "pnpm@" + PNPM_VERSION, "--dir", str(root / "frontend"),
                          "dev", "--host", "127.0.0.1", "--strictPort"], root),
        ]
        for name, command, cwd in commands:
            log = (root / ".dev/logs" / (name + ".log")).open("a")
            logs.append(log)
            child = subprocess.Popen(command, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT,
                                     start_new_session=True)
            children.append(child)
        state["children"] = [{"pid": p.pid, "started": process_start(p.pid)} for p in children]
        atomic_state(state_file, state)
        print("Frontend: http://localhost:" + settings["DEV_FRONTEND_PORT"], flush=True)
        print("Backend:  http://127.0.0.1:" + settings["DEV_BACKEND_PORT"], flush=True)
        print("Private credentials: .dev/.env; logs: .dev/logs/{backend,frontend}.log", flush=True)
        print("Ctrl+C stops native processes; make dev-stop also stops the two development containers.", flush=True)
        ready = set()
        while True:
            for (name, _, _), child in zip(commands, children):
                if child.poll() is not None:
                    raise ValueError(name + " exited; inspect .dev/logs/" + name + ".log")
                if name not in ready:
                    port = settings["DEV_BACKEND_PORT" if name == "backend" else "DEV_FRONTEND_PORT"]
                    try:
                        with urllib.request.urlopen("http://127.0.0.1:" + port + ("/health" if name == "backend" else "/"), timeout=1) as response:
                            if response.status == 200:
                                ready.add(name)
                                print(name + " ready.", flush=True)
                    except OSError:
                        pass
            time.sleep(1)
    finally:
        stop_children(children)
        stop_backend(root, state["session"])
        for log in logs:
            log.close()
        state_file.unlink(missing_ok=True)
        lock.rmdir()


def stop(root):
    state_file = root / ".dev/session.json"
    if state_file.exists():
        state = read_state(state_file)
        if is_our_supervisor(state, root):
            os.kill(state["pid"], signal.SIGTERM)
            for _ in range(120):
                if not state_file.exists():
                    break
                time.sleep(0.1)
            else:
                raise ValueError("Supervisor is still stopping; inspect the logs before retrying")
        else:
            if state.get("root") != str(root):
                raise ValueError("Cannot verify development session ownership; inspect .dev/session.json")
            # Recorded children are checked by PID birth time and project command.
            for child in state.get("children", []):
                stop_record(child, root)
            stop_backend(root, state.get("session"))
            state_file.unlink()
    lock = root / ".dev/session.lock"
    if lock.exists():
        lock.rmdir()
    if (root / ".dev/.env").exists():
        subprocess.run(compose(root) + ["stop"], cwd=root, check=True)
    print("Development processes/dependencies stopped; data retained.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("setup", "up", "stop", "status", "backend-exec"))
    args = parser.parse_args()
    os.umask(0o077)
    def interrupted(_signum, _frame):
        CANCEL.set()
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        if args.command == "backend-exec":
            backend_exec(ROOT)
            return 0
        if args.command == "stop":
            stop(ROOT)
            return 0
        settings = initialize(ROOT)
        env = development_env(ROOT, settings)
        if args.command == "setup":
            setup(ROOT, env)
        elif args.command == "status":
            path = ROOT / ".dev/session.json"
            running = path.exists() and is_our_supervisor(read_state(path), ROOT)
            print("Native development session: " + ("running" if running else "not running"))
            subprocess.run(compose(ROOT) + ["ps"], env=env, cwd=ROOT, check=True)
        else:
            up(ROOT, settings, env)
    except KeyboardInterrupt:
        return 0
    except (ValueError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        print("Development error: " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
