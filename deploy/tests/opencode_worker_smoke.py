"""Check the real worker image on loopback, with all external networking disabled."""
import os
import re
import secrets
import subprocess
import time


def main():
    image = os.environ.get("WORKER_SMOKE_IMAGE", "")
    if not re.fullmatch(
        r"(?:ghcr\.io/kenandkent/sub2api-opencode-free-worker@sha256:[0-9a-f]{64}"
        r"|sub2api-opencode-free-worker:[a-zA-Z0-9_.-]+)", image
    ):
        raise SystemExit("WORKER_SMOKE_IMAGE must be a worker digest or a local worker test tag")
    token = "ci-smoke-" + secrets.token_hex(24)
    environment = dict(os.environ, OPENCODE_FREE_BUN_TOKEN=token)
    containers = []

    def docker(*args, input=None, check=True):
        return subprocess.run(
            ["docker", *args], input=input, env=environment, check=check,
            capture_output=True, text=True, timeout=120,
        )

    hardened = ["--read-only", "--tmpfs", "/tmp", "--cap-drop", "ALL",
                "--security-opt", "no-new-privileges:true"]
    try:
        # A namespace holder models the application's container. No ports or
        # host mounts are created, and neither container has internet access.
        parent = docker("run", "-d", *hardened, "--network", "none",
                        "--env", "OPENCODE_FREE_BUN_TOKEN", "--entrypoint", "sh",
                        image, "-c", "sleep 120").stdout.strip()
        containers.append(parent)
        worker = docker("run", "-d", *hardened, "--network", "container:" + parent,
                        "--env", "OPENCODE_FREE_BUN_TOKEN", "--env",
                        "BUN_RUNTIME_TRANSPILER_CACHE_PATH=0", image).stdout.strip()
        containers.append(worker)
        health = "fetch('http://127.0.0.1:19090/health/ready', {signal: AbortSignal.timeout(2000)}).then(r => process.exit(r.ok ? 0 : 1)).catch(() => process.exit(1))"
        deadline = time.monotonic() + 20
        while docker("exec", worker, "bun", "-e", health, check=False).returncode:
            if time.monotonic() >= deadline:
                raise RuntimeError("worker did not become ready")
            time.sleep(0.25)
        actual_token = docker("exec", parent, "sh", "-c",
                              'printf "%s" "$OPENCODE_FREE_BUN_TOKEN"').stdout
        if actual_token != token:
            raise RuntimeError("namespace holder did not receive the fixture token")
        for credential, status in [(actual_token, 400), ("incorrect-fixture-token", 401)]:
            probe = (
                "const token = await Bun.stdin.text(); "
                "const r = await fetch('http://127.0.0.1:19090/v1/transport/chat', "
                "{method:'POST',headers:{authorization:'Bearer '+token},body:'{}',"
                "signal:AbortSignal.timeout(5000)}); "
                f"if (r.status !== {status} || r.headers.get('x-sub2api-dispatched') !== 'false') process.exit(1)"
            )
            docker("exec", "-i", worker, "bun", "-e", probe, input=credential)
        print("PASS worker readiness, shared namespace, valid/invalid authentication and no dispatch")
    finally:
        for container in reversed(containers):
            docker("rm", "-f", container, check=False)


if __name__ == "__main__":
    main()
