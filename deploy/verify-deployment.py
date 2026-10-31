#!/usr/bin/env python3
"""Read-only verification of release state against actual local Docker images.

Run on the deployment host; never loads .env, pulls images, or changes containers.
An explicit release triple verifies a pending deployment before state is committed.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

RELEASE = re.compile(r"[0-9a-f]{40}-[0-9]+-[0-9]+")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}")
APP = re.compile(r"ghcr\.io/kenandkent/sub2api@sha256:[0-9a-f]{64}")
WORKER = re.compile(r"ghcr\.io/kenandkent/sub2api-opencode-free-worker@sha256:[0-9a-f]{64}")


class VerificationError(Exception):
    pass


def docker(args):
    # Only explicitly selected public identity fields are requested. Docker's
    # stderr can include daemon/context details; do not echo it into CI logs.
    try:
        return subprocess.run(["docker", *args], check=True, capture_output=True,
                              text=True, timeout=30).stdout.strip()
    except (OSError, subprocess.SubprocessError) as error:
        raise VerificationError("Docker identity query failed; inspect the local daemon and retained release manually") from error


def fields(output, count):
    try:
        result = json.loads(output)
    except (ValueError, TypeError) as error:
        raise VerificationError("Docker returned invalid identity fields") from error
    if not isinstance(result, list) or len(result) != count:
        raise VerificationError("Docker returned incomplete identity fields")
    return result


def containers(service, running_only=False):
    return docker(["ps", "-q" if running_only else "-aq",
                   "--filter", "label=com.docker.compose.project=mysubapi",
                   "--filter", "label=com.docker.compose.service=" + service]).split()


def verify_service(service, reference, sha):
    ids = containers(service)
    if len(ids) != 1:
        raise VerificationError(f"{service}: expected exactly one container; inspect Compose project mysubapi")
    actual_id, running = fields(docker(["container", "inspect", "--format",
        '[{{json .Image}},{{json .State.Running}}]', ids[0]]), 2)
    if running is not True:
        raise VerificationError(f"{service}: container is not running; inspect health and retained release")
    expected_id, revision, dirty, source = fields(docker(["image", "inspect", "--format",
        '[{{json .Id}},{{json (index .Config.Labels "org.opencontainers.image.revision")}},'
        '{{json (index .Config.Labels "io.sub2api.source.dirty")}},'
        '{{json (index .Config.Labels "io.sub2api.source.digest")}}]', reference]), 4)
    if not isinstance(expected_id, str) or not DIGEST.fullmatch(expected_id):
        raise VerificationError(f"{service}: expected digest has no valid local image ID; inspect the pinned release image")
    if actual_id != expected_id:
        raise VerificationError(f"{service}: image ID mismatch against {reference}; restore the intended immutable release")
    if revision != sha:
        raise VerificationError(f"{service}: OCI revision mismatch against release SHA {sha}; rebuild or select the matching release")
    # Older CI releases supplied OCI revision but no custom source labels.
    # They remain verifiable by digest/revision; explicit dirty/unknown labels fail.
    if dirty not in (None, "", "false"):
        raise VerificationError(f"{service}: source is dirty or unrecognized; production requires a clean release")
    if source not in (None, "") and (not isinstance(source, str) or not DIGEST.fullmatch(source)):
        raise VerificationError(f"{service}: invalid source digest label")
    legacy = dirty in (None, "")
    print(f"{service}: running image ID and OCI revision match" + (" (legacy source labels absent)" if legacy else ""))
    return source or None


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("deploy_root", type=Path)
    parser.add_argument("--release")
    parser.add_argument("--image")
    parser.add_argument("--worker", help="immutable worker digest, or '-' for no worker")
    args = parser.parse_args(argv)
    try:
        supplied = [args.release, args.image, args.worker]
        if any(value is not None for value in supplied):
            if not all(value is not None for value in supplied):
                raise VerificationError("provide --release, --image and --worker together")
            release, image, worker = supplied
        else:
            try:
                lines = (args.deploy_root / "current-deployment").read_text().splitlines()
            except OSError as error:
                raise VerificationError("cannot read current-deployment; specify a complete pending release triple") from error
            if len(lines) != 1 or len(lines[0].split()) not in (2, 3):
                raise VerificationError("invalid current-deployment: expected one line with two or three fields")
            state = lines[0].split()
            release, image = state[:2]
            worker = state[2] if len(state) == 3 else "-"
        if not RELEASE.fullmatch(release) or not APP.fullmatch(image) or (worker != "-" and not WORKER.fullmatch(worker)):
            raise VerificationError("invalid release identity: use SHA-RUN_ID-RUN_ATTEMPT and immutable repository digests")
        if os.environ.get("DOCKER_HOST") or os.environ.get("DOCKER_CONTEXT", "default") != "default" or docker(["context", "show"]) != "default":
            raise VerificationError("only the default local Docker context is supported")
        source = verify_service("sub2api", image, release[:40])
        if worker != "-":
            worker_source = verify_service("opencode-free-worker", worker, release[:40])
            if source != worker_source:
                raise VerificationError("app/worker source digest mismatch; deploy both images from the same source")
        elif containers("opencode-free-worker", running_only=True):
            raise VerificationError("unexpected running opencode-free-worker while release state disables the worker; inspect retained release")
        print(f"Deployment identity verified: {release}")
        return 0
    except VerificationError as error:
        print(f"Deployment identity error: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
