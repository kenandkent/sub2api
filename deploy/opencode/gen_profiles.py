#!/usr/bin/env python3
"""Generate the pinned OpenCode free compatibility profiles for every official free model.

The audited templates are model-neutral: only ``id``, ``model`` and ``status``
differ between models. Generation is therefore a raw byte substitution so the
JSON formatting (and the frozen big-pickle assets) never churn. The worker
source table (manifest.generated.ts) is emitted alongside so the Bun worker pins
the same ids/hashes the Go gateway registry carries.

Usage:
    python3 -B deploy/opencode/gen_profiles.py --out deploy/opencode/profiles
    python3 -B deploy/opencode/gen_profiles.py --out <omniroute>/services/opencode-free-bun/profiles
"""
import argparse
import hashlib
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
TEXT_TEMPLATE = ROOT / "deploy/opencode/balanced-text-profile.v2.json"
# The template and the two captured special-case profiles live in the committed
# profiles directory so regeneration never depends on the gitignored .cache/.
PROFILES = ROOT / "deploy/opencode/profiles"
AGENT_TEMPLATE = PROFILES / "big-pickle-agent-balanced.v1.json"
WORKER_MANIFEST = ROOT / "deploy/opencode/worker/manifest.generated.ts"

# Official free chat models (https://dev.opencode.ai/v2/docs/console/models).
MODELS = [
    "big-pickle",
    "glm-5.3-flash-free",
    "deepseek-v4.1-flash-free",
    "beam-free",
    "flint-40b-free",
    "mimo-v2.6-flash-free",
    "nemotron-3-ultra-free",
    "nemotron-3.5-lightning-free",
    "nemotron-3.5-super-vl-free",
]

BIG_PICKLE = "big-pickle"
MIMO = "mimo-v2.6-flash-free"
STATUS_NEW = "candidate-unvalidated"


def substitute(raw: bytes, template_id: str, new_id: str, model: str, status: str) -> bytes:
    text = raw.decode("utf-8")
    if text.count(f'"{template_id}"') != 1:
        raise ValueError("template id not unique")
    text = text.replace(f'"{template_id}"', f'"{new_id}"')
    if text.count('"status": ') != 1:
        raise ValueError("template status not unique")
    head, sep, tail = text.partition('"status": ')
    value_end = tail.index('"', 1)
    text = head + sep + f'"{status}"' + tail[value_end + 1:]
    if text.count("big-pickle") != 2:
        raise ValueError("unexpected big-pickle occurrences")
    text = text.replace("big-pickle", model)
    return text.encode("utf-8")


def normalized(data: dict) -> dict:
    data = json.loads(json.dumps(data))
    data.pop("id", None)
    data.pop("status", None)
    data.pop("model", None)
    data.get("body", {}).pop("model", None)
    return data


def build() -> dict[str, bytes]:
    text_template_raw = TEXT_TEMPLATE.read_bytes()
    agent_template_raw = AGENT_TEMPLATE.read_bytes()
    text_template = json.loads(text_template_raw)
    agent_template = json.loads(agent_template_raw)
    files: dict[str, bytes] = {}
    for model in MODELS:
        if model == BIG_PICKLE:
            text_id, text_status, text_raw = text_template["id"], None, text_template_raw
            text_ref = text_template
        elif model == MIMO:
            mimo_raw = (PROFILES / f"{model}-text-balanced.v1.json").read_bytes()
            text_id = f"opencode-{model}-text-balanced-20261027-v1"
            text_status, text_raw, text_ref = None, mimo_raw, json.loads(mimo_raw)
        else:
            text_id = f"opencode-{model}-text-balanced-20261027-v1"
            text_status = STATUS_NEW
            text_raw = substitute(
                text_template_raw, text_template["id"], text_id, model, text_status
            )
            text_ref = text_template
        if model == BIG_PICKLE:
            agent_id, agent_status, agent_raw = agent_template["id"], None, agent_template_raw
            agent_ref = agent_template
        elif model == MIMO:
            mimo_raw = (PROFILES / f"{model}-agent-balanced.v1.json").read_bytes()
            agent_id = f"opencode-{model}-agent-balanced-20261027-v1"
            agent_status, agent_raw, agent_ref = None, mimo_raw, json.loads(mimo_raw)
        else:
            agent_id = f"opencode-{model}-agent-balanced-20261027-v1"
            agent_status = STATUS_NEW
            agent_raw = substitute(
                agent_template_raw, agent_template["id"], agent_id, model, agent_status
            )
            agent_ref = agent_template
        text_name = (
            f"{model}-text-balanced.v2.json"
            if model == BIG_PICKLE
            else f"{model}-text-balanced.v1.json"
        )
        agent_name = f"{model}-agent-balanced.v1.json"
        files[text_name] = text_raw
        files[agent_name] = agent_raw
        verify(text_raw, text_ref, text_id, model, text_status)
        verify(agent_raw, agent_ref, agent_id, model, agent_status)
    return files


def verify(raw: bytes, template: dict, expect_id: str, model: str, status: str | None) -> None:
    data = json.loads(raw)
    if data["id"] != expect_id:
        raise ValueError(f"bad id {data['id']} != {expect_id}")
    if data["model"] != model or data["body"]["model"] != model:
        raise ValueError(f"bad model for {expect_id}")
    if status is not None and data["status"] != status:
        raise ValueError(f"bad status for {expect_id}")
    if data["endpoint"] != template["endpoint"]:
        raise ValueError(f"endpoint drift for {expect_id}")
    if data["headers"] != template["headers"]:
        raise ValueError(f"header drift for {expect_id}")
    if data["runtime"] != template["runtime"]:
        raise ValueError(f"runtime drift for {expect_id}")
    if normalized(data) != normalized(template):
        raise ValueError(f"template drift for {expect_id}")


def emit_manifest(grouped: dict[str, dict[str, dict]], path: Path) -> None:
    """Write the worker's source-pinned profile table (ids, hashes, file names)."""
    lines = [
        "// Code generated by deploy/opencode/gen_profiles.py - DO NOT EDIT.",
        "/** Pinned profile bytes; the model-neutral text/agent templates are the big-pickle entries. */",
        "export const FREE_BUN_PROFILES = {",
    ]
    for model in sorted(grouped):
        modes = grouped[model]
        if set(modes) != {"text", "agent"}:
            raise ValueError(f"incomplete profile modes for {model}: {sorted(modes)}")
        text, agent = modes["text"], modes["agent"]
        lines.append(f'  "{model}": {{')
        lines.append(f'    id: "{text["id"]}",')
        lines.append(f'    sha256: "{text["sha256"]}",')
        lines.append(f'    file: "{text["file"]}",')
        lines.append("    agent: {")
        lines.append(f'      id: "{agent["id"]}",')
        lines.append(f'      sha256: "{agent["sha256"]}",')
        lines.append(f'      file: "{agent["file"]}",')
        lines.append("    },")
        lines.append("  },")
    lines.append("} as const;")
    lines.append("")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(lines), encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, help="output directory for profile JSON files")
    parser.add_argument("--manifest", default=str(WORKER_MANIFEST),
                        help="worker manifest.generated.ts path (empty string skips emission)")
    args = parser.parse_args()
    out = Path(args.out)
    if not out.is_absolute():
        out = ROOT / out
    files = build()
    out.mkdir(parents=True, exist_ok=True)
    manifest = {}
    grouped: dict[str, dict[str, dict]] = {}
    for name in sorted(files):
        raw = files[name]
        target = out / name
        if target.exists() and target.read_bytes() != raw:
            raise ValueError(f"refusing to overwrite changed profile {target}")
        target.write_bytes(raw)
        digest = hashlib.sha256(raw).hexdigest()
        data = json.loads(raw)
        mode = "text" if "-text-balanced" in name else "agent"
        manifest[data["id"]] = {"file": name, "model": data["model"], "sha256": digest}
        grouped.setdefault(data["model"], {})[mode] = {
            "id": data["id"], "file": name, "sha256": digest
        }
    if args.manifest:
        manifest_path = Path(args.manifest)
        if not manifest_path.is_absolute():
            manifest_path = ROOT / manifest_path
        emit_manifest(grouped, manifest_path)
        print(f"wrote {len(grouped)} model entries to {manifest_path}", file=sys.stderr)
    for profile_id in sorted(manifest):
        entry = manifest[profile_id]
        print(f'{entry["model"]}\t{profile_id}\t{entry["sha256"]}\t{entry["file"]}')
    print(f"wrote {len(files)} profiles to {out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
