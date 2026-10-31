#!/usr/bin/env python3
"""Read-only local release baseline, or explicit paired app/worker rebuild.

The fingerprint covers Git-tracked and nonignored untracked source, including
paths, executable bits, deletions and symlink targets. Ignored private runtime
data is deliberately excluded. It identifies this checkout, not a clean release.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
SERVICES = ("sub2api", "opencode-free-worker")
LABEL_KEYS = (
    "org.opencontainers.image.revision", "org.opencontainers.image.version",
    "io.sub2api.source.digest", "io.sub2api.source.dirty",
)


class BaselineError(ValueError):
    """A diagnostic authored here, safe to display without command output."""


def command_stage(command):
    if command[:2] == ["docker", "compose"]:
        if "build" in command:
            return "paired image build"
        if "up" in command:
            return "paired container recreation"
        return "Compose container discovery"
    if command[:2] == ["docker", "context"]:
        return "local Docker endpoint preflight"
    if command[:3] == ["docker", "image", "inspect"]:
        return "immutable image metadata inspection"
    return "container ownership/runtime inspection"


def label_template():
    keys = (*LABEL_KEYS, "com.docker.compose.project", "com.docker.compose.project.working_dir")
    return "{" + ",".join(json.dumps(key) + ':{{json (index .Config.Labels ' + json.dumps(key) + ')}}'
                           for key in keys) + "}"


CONTAINER_FORMAT = ('{"id":{{json .Id}},"image_id":{{json .Image}},'
                    '"image":{{json .Config.Image}},"running":{{json .State.Running}},'
                    '"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}"missing"{{end}},'
                    '"network_mode":{{json .HostConfig.NetworkMode}},"mounts":{{json .Mounts}},"labels":' + label_template() + '}')
IMAGE_FORMAT = ('{"repo_digests":{{json .RepoDigests}},"image_labels":' + label_template() + '}')


def source_identity(root):
    def git(*args):
        result = subprocess.run(["git", "-C", str(root), *args], capture_output=True,
                                env={**os.environ, "GIT_OPTIONAL_LOCKS": "0"})
        if result.returncode:
            raise BaselineError("Cannot inspect Git source identity")
        return result.stdout
    commit = git("rev-parse", "HEAD").decode().strip()
    if not re.fullmatch(r"[0-9a-f]{40,64}", commit):
        raise BaselineError("Invalid Git revision")
    tree = {}
    for entry in git("ls-tree", "-r", "-z", "HEAD").split(b"\0"):
        if entry:
            metadata, relative = entry.split(b"\t", 1)
            mode, kind, oid = metadata.split()
            tree[relative] = (mode, kind, oid)
    visible = set(git("ls-files", "--cached", "--others", "--exclude-standard", "-z").split(b"\0")) - {b""}
    paths = sorted(visible | tree.keys())
    object_format = git("rev-parse", "--show-object-format").decode().strip()
    if object_format not in ("sha1", "sha256"):
        raise BaselineError("Unsupported Git object format")
    dirty = bool(git("status", "--porcelain=v1", "--untracked-files=all"))
    digest = hashlib.sha256()
    for relative in paths:
        path = root / os.fsdecode(relative)
        # Reading a tracked file via a symlinked parent could escape the project.
        if not path.parent.resolve().is_relative_to(root.resolve()):
            raise BaselineError("Source path escapes the project root")
        digest.update(len(relative).to_bytes(8, "big") + relative)
        try:
            info = path.lstat()
        except FileNotFoundError:
            digest.update(b"deleted\0")
            dirty = True
            continue
        if stat.S_ISLNK(info.st_mode):
            payload = os.fsencode(os.readlink(path))
            digest.update(b"symlink\0" + len(payload).to_bytes(8, "big") + payload)
            blob = hashlib.new(object_format, b"blob " + str(len(payload)).encode() + b"\0" + payload)
            mode = b"120000"
        elif stat.S_ISREG(info.st_mode):
            digest.update(b"executable\0" if info.st_mode & 0o111 else b"file\0")
            content = hashlib.sha256()
            blob = hashlib.new(object_format, b"blob " + str(info.st_size).encode() + b"\0")
            with path.open("rb") as stream:
                for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                    content.update(chunk)
                    blob.update(chunk)
            digest.update(content.digest())
            mode = b"100755" if info.st_mode & 0o111 else b"100644"
        else:
            raise BaselineError("Unsupported source input (submodules require a separate identity)")
        if tree.get(relative) != (mode, b"blob", blob.hexdigest().encode()):
            dirty = True
    try:
        version = (root / "backend/cmd/server/VERSION").read_text().strip()
    except OSError:
        raise BaselineError("Cannot read backend/cmd/server/VERSION") from None
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.+_-]*", version):
        raise BaselineError("Invalid backend/cmd/server/VERSION")
    fingerprint = digest.hexdigest()
    return {"commit": commit, "digest": "sha256:" + fingerprint,
            "dirty": str(dirty).lower(),
            "version": version + ("-dirty." + fingerprint[:12] if dirty else "")}


def identity_labels(identity):
    return dict(zip(LABEL_KEYS, (identity["commit"], identity["version"],
                                identity["digest"], identity["dirty"])))


def validation_errors(report):
    errors = []
    expected = identity_labels(report["source"])
    for service in SERVICES:
        container = report["containers"].get(service)
        if not container:
            errors.append(service + ": container missing")
            continue
        if not container.get("running") or container.get("health") != "healthy":
            errors.append(service + ": health is not healthy/running")
        if container.get("image_metadata_available") is False:
            errors.append(service + ": immutable running-image metadata unavailable; release identity unverified")
        for location in ("labels", "image_labels"):
            for key, value in expected.items():
                if container.get(location, {}).get(key) != value:
                    errors.append(service + ": " + location + " " + key + " mismatch or missing")
    app = report["containers"].get("sub2api")
    worker = report["containers"].get("opencode-free-worker")
    if app and worker and worker.get("network_mode") != "container:" + app["id"]:
        errors.append("opencode-free-worker: network namespace does not reference current app")
    binary = report.get("app_binary", {})
    if any(binary.get(key) != report["source"][key] for key in ("version", "commit")):
        errors.append("sub2api: running binary version/commit mismatch or unavailable")
    return errors


class Runtime:
    def __init__(self, root=ROOT):
        self.root = root.resolve()
        # deploy/.env is authoritative for application settings. In particular,
        # a shell password or COMPOSE_REMOVE_ORPHANS must not change this stack.
        allowed = {"PATH", "HOME", "USER", "LOGNAME", "LANG", "TERM", "NO_COLOR",
                   "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
                   "http_proxy", "https_proxy", "all_proxy", "no_proxy",
                   "SSL_CERT_FILE", "SSL_CERT_DIR"}
        self.env = {key: value for key, value in os.environ.items()
                    if key in allowed or key.startswith(("DOCKER_", "LC_"))}
        self.env["GIT_OPTIONAL_LOCKS"] = "0"
        self.compose = ["docker", "compose", "--project-directory", str(self.root / "deploy"),
                        "--project-name", "deploy", "--env-file", str(self.root / "deploy/.env"),
                        "-f", str(self.root / "deploy/docker-compose.yml"),
                        "-f", str(self.root / "deploy/docker-compose.opencode-free.yml")]

    def run(self, command, timeout=120):
        try:
            result = subprocess.run(command, cwd=self.root, env=self.env, capture_output=True,
                                    text=True, timeout=timeout)
        except (OSError, subprocess.TimeoutExpired):
            raise BaselineError("Command unavailable or timed out during " + command_stage(command) +
                                "; command output withheld") from None
        if result.returncode:
            raise BaselineError("Command failed during " + command_stage(command) + " (exit " + str(result.returncode) +
                                "); output withheld to protect configuration")
        return result.stdout

    def inspect_container(self, cid, include_image=True):
        data = json.loads(self.run(["docker", "inspect", "--type", "container", "--format", CONTAINER_FORMAT, cid]))
        if include_image:
            try:
                image = json.loads(self.run(["docker", "image", "inspect", "--format", IMAGE_FORMAT, data["image_id"]]))
                data.update(image)
                data["image_metadata_available"] = True
            except (ValueError, KeyError, TypeError):
                # A completed build may move the tag and remove the old image
                # metadata while its container remains running. Never substitute
                # the mutable tag for that container's actual immutable image ID.
                data.update(image_labels={}, repo_digests=[], image_metadata_available=False)
        return data

    def report(self):
        self.assert_local_docker()
        report = {"source": source_identity(self.root), "containers": {}, "app_binary": {},
                  "native": {"configuration_present": (self.root / ".dev/.env").exists(),
                             "session_record_present": (self.root / ".dev/session.json").exists(),
                             "identity": "native development has no verified release identity"}}
        for service in SERVICES:
            ids = self.run([*self.compose, "ps", "--all", "--quiet", service]).split()
            if len(ids) > 1:
                raise BaselineError("Multiple local containers for " + service)
            if ids:
                report["containers"][service] = self.inspect_container(ids[0])
        app = report["containers"].get("sub2api")
        if app and app["running"]:
            try:
                # Go's log package writes -version to stderr; redirect inside the
                # process runner without invoking a shell or printing raw output.
                result = subprocess.run(["docker", "exec", app["id"], "/app/sub2api", "-version"],
                                        cwd=self.root, env=self.env, capture_output=True, text=True, timeout=15)
                match = re.search(r"Sub2API ([A-Za-z0-9.+_-]+) \(commit: ([A-Za-z0-9._-]+), built: ([^\r\n)]+)\)",
                                  result.stdout + result.stderr)
                if result.returncode == 0 and match:
                    report["app_binary"] = {"version": match[1], "commit": match[2], "built": match[3]}
            except (OSError, subprocess.TimeoutExpired):
                pass
        report["errors"] = validation_errors(report)
        return report

    def assert_local_docker(self):
        host = self.env.get("DOCKER_HOST")
        if not host or self.env.get("DOCKER_CONTEXT"):
            host = json.loads(self.run(["docker", "context", "inspect", "--format", "{{json .Endpoints.docker.Host}}"])).strip()
        if not host.startswith(("unix://", "npipe://")):
            raise BaselineError("Local baseline requires a local Docker daemon; remote Docker endpoints are refused")

    def assert_ownership(self):
        # The explicit fixed app name must belong to this checkout/project before
        # Compose may recreate anything. An absent name is safe for first start.
        expected = (("sub2api", "deploy_sub2api_data", "/app/data"),
                    ("sub2api-postgres", "deploy_postgres_data", "/var/lib/postgresql/data"),
                    ("sub2api-redis", "deploy_redis_data", "/data"))
        for name, volume, destination in expected:
            cid = self.run(["docker", "ps", "--all", "--quiet", "--filter", "name=^/" + name + "$"]).strip()
            if not cid:
                raise BaselineError("Existing complete local stack required; missing " + name)
            container = self.inspect_container(cid, include_image=False)
            labels = container["labels"]
            if labels.get("com.docker.compose.project") != "deploy" or labels.get("com.docker.compose.project.working_dir") != str(self.root / "deploy"):
                raise BaselineError("Existing container is not owned by this local checkout/project")
            mounts = container.get("mounts", [])
            data_mounts = [item for item in mounts if item.get("Destination") == destination]
            if len(data_mounts) != 1 or any(data_mounts[0].get(key) != value for key, value in
                                          (("Type", "volume"), ("Name", volume), ("RW", True))):
                raise BaselineError("Existing " + name + " data mount does not match preserved named-volume stack")
            if name == "sub2api" and len(mounts) != 1:
                raise BaselineError("Existing app has additional mounts; preserve custom configuration manually before rebuilding")

    def verify_built_images(self, identity):
        expected = identity_labels(identity)
        for image in ("sub2api:mysubapi", "deploy-opencode-free-worker"):
            data = json.loads(self.run(["docker", "image", "inspect", "--format", IMAGE_FORMAT, image]))
            if any(data.get("image_labels", {}).get(key) != value for key, value in expected.items()):
                raise BaselineError("Built image identity mismatch; existing services have not been restarted")

    def rebuild(self, timeout=240):
        self.assert_local_docker()
        self.assert_ownership()
        identity = source_identity(self.root)
        cache = self.root / ".cache/local-runtime"
        if not cache.resolve().is_relative_to(self.root):
            raise BaselineError("Local runtime cache escapes the project root")
        for name in ("tmp", "buildx"):
            if not (cache / name).resolve().is_relative_to(self.root):
                raise BaselineError("Local runtime cache escapes the project root")
            (cache / name).mkdir(parents=True, exist_ok=True)
        self.env.update(TMPDIR=str(cache / "tmp"), BUILDX_CONFIG=str(cache / "buildx"))
        args = {"COMMIT": identity["commit"], "VERSION": identity["version"],
                "SOURCE_DIGEST": identity["digest"], "SOURCE_DIRTY": identity["dirty"],
                "DATE": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")}
        command = [*self.compose, "build"]
        for key, value in args.items():
            command += ["--build-arg", key + "=" + value]
        self.run([*command, *SERVICES], timeout=3600)
        if source_identity(self.root) != identity:
            raise BaselineError("Source changed during build; services have not been restarted; rebuild again")
        self.verify_built_images(identity)
        # Force both containers: the worker shares the app's network namespace.
        # No down/rm/volume operation and no dependencies are recreated.
        self.run([*self.compose, "up", "--detach", "--no-deps", "--no-build", "--pull", "never",
                  "--force-recreate", *SERVICES], timeout=180)
        deadline = time.monotonic() + timeout
        while True:
            report = self.report()
            if not report["errors"]:
                return report
            if time.monotonic() >= deadline:
                return report
            time.sleep(min(2, max(0, deadline - time.monotonic())))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("report", "check", "rebuild"), nargs="?", default="report")
    parser.add_argument("--json", action="store_true", help="Print only allowlisted baseline fields")
    parser.add_argument("--timeout", type=int, default=240, help="Rebuild health wait in seconds (default 240)")
    options = parser.parse_args(argv)
    if options.timeout < 1:
        parser.error("--timeout must be positive")
    try:
        runtime = Runtime()
        report = runtime.rebuild(options.timeout) if options.action == "rebuild" else runtime.report()
        if options.json:
            print(json.dumps(report, indent=2, sort_keys=True))
        else:
            source = report["source"]
            print("Source: " + source["commit"] + " dirty=" + source["dirty"] + " version=" + source["version"])
            print("Source fingerprint: " + source["digest"])
            for name in SERVICES:
                item = report["containers"].get(name)
                if not item:
                    print(name + ": absent")
                    continue
                print(name + ": container=" + item["id"] + " image=" + item["image_id"] + " health=" + item["health"])
                print("  identity=" + json.dumps({key: item["image_labels"].get(key) for key in LABEL_KEYS}, sort_keys=True))
                print("  repo_digests=" + json.dumps(item["repo_digests"]) + " network=" + item["network_mode"])
            print("App binary: " + json.dumps(report["app_binary"], sort_keys=True))
            print("Native development: " + json.dumps(report["native"], sort_keys=True))
            for error in report["errors"]:
                print("MISMATCH: " + error)
            print("Baseline: " + ("mismatch" if report["errors"] else "verified"))
        return int(bool(report["errors"]) and options.action != "report")
    except BaselineError as error:
        print("Local baseline failed: " + str(error), file=sys.stderr)
        return 1
    except (ValueError, KeyError, TypeError, OSError):
        # Do not accidentally render untrusted command/Compose/environment data.
        print("Local baseline failed: configuration, ownership, source or Docker inspection error; inspect privately.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
