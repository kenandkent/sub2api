#!/usr/bin/env python3
"""Foreground supervisor for standard LiteLLM; suitable for launchd or systemd."""
import argparse
import os
from pathlib import Path
import signal
import subprocess

os.umask(0o077)
HERE = Path(__file__).resolve().parent
parser = argparse.ArgumentParser()
parser.add_argument("--env-file", type=Path, default=HERE / "runtime.env")
args = parser.parse_args()
values = {}
for line in args.env_file.read_text().splitlines():
    line = line.strip()
    if not line or line.startswith("#"):
        continue
    name, separator, value = line.partition("=")
    if not separator or name not in {"SUB2API_PRODUCTION_MIMO_API_KEY", "SUB2API_MIMO_UPSTREAM_BASE", "SUB2API_MIMO_BRIDGE_PORT"}:
        raise SystemExit("Unsupported runtime env entry")
    values[name] = value
key = values.get("SUB2API_PRODUCTION_MIMO_API_KEY", "")
if not key:
    raise SystemExit("Missing bridge API key")
port = int(values.get("SUB2API_MIMO_BRIDGE_PORT", "19088"))
runtime = HERE / "runtime"
for name in ("", "tmp", "cache", "config", "data", "tiktoken", "hf"):
    path = runtime / name
    path.mkdir(parents=True, exist_ok=True)
    path.chmod(0o700)
keep = {"PATH", "HOME", "USER", "LOGNAME", "LANG", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}
env = {k: v for k, v in os.environ.items() if k in keep or k.startswith("LC_")}
env.update(values)
env.setdefault("SUB2API_MIMO_UPSTREAM_BASE", "https://sub2api.vseeu.de/v1")
env.update({"PYTHONDONTWRITEBYTECODE": "1", "TMPDIR": str(runtime / "tmp"), "TMP": str(runtime / "tmp"), "TEMP": str(runtime / "tmp"), "XDG_CACHE_HOME": str(runtime / "cache"), "XDG_CONFIG_HOME": str(runtime / "config"), "XDG_DATA_HOME": str(runtime / "data"), "TIKTOKEN_CACHE_DIR": str(runtime / "tiktoken"), "HF_HOME": str(runtime / "hf"), "HUGGINGFACE_HUB_CACHE": str(runtime / "hf"), "LITELLM_LOCAL_MODEL_COST_MAP": "True", "LITELLM_LOG": "ERROR", "LITELLM_TELEMETRY": "False", "DO_NOT_TRACK": "1", "TOKENIZERS_PARALLELISM": "false", "NO_PROXY": "127.0.0.1,localhost,::1", "no_proxy": "127.0.0.1,localhost,::1"})
command = [str(HERE / "venv/bin/python"), "-m", "litellm.proxy.proxy_cli", "--config", str(HERE / "config.yaml"), "--host", "127.0.0.1", "--port", str(port), "--num_workers", "1", "--telemetry", "False"]
child = subprocess.Popen(command, cwd=HERE, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)
def stop(_signal, _frame):
    child.terminate()
signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)
with (HERE / "proxy.redacted.log").open("a", buffering=1) as output:
    for line in child.stdout:
        output.write(line.replace(key, "[REDACTED]"))
raise SystemExit(child.wait())
