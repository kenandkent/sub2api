"""Start the bundled release image on loopback with external networking disabled."""
import os
import re
import secrets
import subprocess
import time


READINESS = """
const app = await fetch('http://127.0.0.1:8080/setup/status', {
  signal: AbortSignal.timeout(2000),
});
if (!app.ok || (await app.json()).data.needs_setup !== true) {
  throw new Error('Sub2API setup server is not ready');
}
const worker = await fetch('http://127.0.0.1:19090/health/ready', {
  signal: AbortSignal.timeout(2000),
});
if (!worker.ok) throw new Error('Worker is not ready');
"""

AUTHENTICATION = """
const token = process.env.OPENCODE_FREE_BUN_TOKEN;
if (!token) throw new Error('Worker fixture token is missing');
for (const [credential, status] of [[token, 400], ['incorrect-fixture-token', 401]]) {
  const response = await fetch('http://127.0.0.1:19090/v1/transport/chat', {
    method: 'POST',
    headers: { authorization: 'Bearer ' + credential },
    body: '{}',
    signal: AbortSignal.timeout(5000),
  });
  if (response.status !== status || response.headers.get('x-sub2api-dispatched') !== 'false') {
    throw new Error(`Expected status ${status} without dispatch, got ${response.status}`);
  }
}
"""


def main():
    image = os.environ.get("BUNDLED_SMOKE_IMAGE", "")
    if not re.fullmatch(
        r"(?:ghcr\.io/kenandkent/sub2api@sha256:[0-9a-f]{64}"
        r"|sub2api:[a-zA-Z0-9_.-]+)", image
    ):
        raise SystemExit("BUNDLED_SMOKE_IMAGE must be a release digest or a local sub2api test tag")
    token = "ci-smoke-" + secrets.token_hex(24)
    environment = dict(os.environ, OPENCODE_FREE_BUN_TOKEN=token)

    def docker(*args, check=True, timeout=120):
        result = subprocess.run(
            ["docker", *args], env=environment, capture_output=True, text=True, timeout=timeout,
        )
        if check and result.returncode:
            raise RuntimeError(f"docker {args[0]} failed: {result.stderr.strip()}")
        return result

    if "@" in image:
        docker("pull", image, timeout=300)
    mode = docker("image", "inspect", "--format",
                  '{{ index .Config.Labels "io.sub2api.opencode-free.mode" }}', image).stdout.strip()
    if mode != "bundled":
        raise RuntimeError("Image does not have the bundled OpenCode runtime label")

    # With fresh temporary data and AUTO_SETUP disabled, the default entrypoint
    # starts its worker and Go serves the setup wizard without Postgres or Redis.
    # There are no published ports, host mounts, or external routes.
    container = docker(
        "run", "-d", "--network", "none", "--read-only", "--tmpfs", "/tmp", "--tmpfs", "/app/data",
        "--security-opt", "no-new-privileges:true", "--env", "AUTO_SETUP=false",
        "--env", "SERVER_HOST=127.0.0.1", "--env", "SERVER_PORT=8080",
        "--env", "OPENCODE_FREE_BUN_ENABLED=true", "--env", "OPENCODE_FREE_BUN_TOKEN", image,
    ).stdout.strip()
    try:
        deadline = time.monotonic() + 30
        while True:
            result = docker("exec", container, "bun", "-e", READINESS, check=False)
            if result.returncode == 0:
                break
            running = docker("inspect", "--format", "{{.State.Running}}", container).stdout.strip()
            if running != "true" or time.monotonic() >= deadline:
                raise RuntimeError(f"Bundled app and worker did not become ready: {result.stderr.strip()}")
            time.sleep(0.25)
        docker("exec", container, "bun", "-e", AUTHENTICATION)
        docker("stop", "--time", "15", container)
        exit_code = docker("inspect", "--format", "{{.State.ExitCode}}", container).stdout.strip()
        if exit_code != "0":
            raise RuntimeError(f"Bundled app exited with code {exit_code} during shutdown")
        print("PASS bundled app and worker startup, authentication, no dispatch, and shutdown")
    except Exception:
        logs = docker("logs", "--tail", "60", container, check=False)
        print((logs.stdout + logs.stderr).replace(token, "[fixture token]"), end="")
        raise
    finally:
        docker("rm", "-f", container, check=False)


if __name__ == "__main__":
    main()
