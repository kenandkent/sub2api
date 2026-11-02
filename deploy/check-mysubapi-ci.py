#!/usr/bin/env python3
"""Confirm that GitHub created this commit's release workflow; never dispatch it.

Finding a queued run proves a trigger, not a deployment. --completed requires a
successful completed workflow; production identity still needs its verifier.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = "kenandkent/sub2api"
WORKFLOW = ".github/workflows/mysubapi-ci-cd.yml"


class APIError(Exception):
    pass


class NoRun(Exception):
    pass


class RunPending(Exception):
    pass


def select_run(payload, sha):
    if not isinstance(payload, dict) or not isinstance(payload.get("workflow_runs"), list):
        raise APIError("GitHub returned an invalid workflow list")
    matches = []
    for run in payload["workflow_runs"]:
        if not isinstance(run, dict):
            raise APIError("GitHub returned an invalid workflow entry")
        if (run.get("head_sha") == sha and run.get("head_branch") == "mysubapi"
                and run.get("path") == WORKFLOW and run.get("event") in ("push", "workflow_dispatch")):
            if not isinstance(run.get("id"), int) or run["id"] <= 0:
                raise APIError("GitHub returned an invalid run identity")
            matches.append(run)
    return max(matches, key=lambda run: run["id"]) if matches else None


def wait_for_run(fetch, sha, timeout, interval, completed=False, clock=time.monotonic, pause=time.sleep):
    deadline = clock() + timeout
    while True:
        run = select_run(fetch(), sha)
        if run is not None and (not completed or run.get("status") == "completed"):
            return run
        remaining = deadline - clock()
        if remaining <= 0:
            if run is not None:
                raise RunPending("Workflow exists but has not completed within the wait window")
            raise NoRun("No release workflow exists for this SHA; push success is not CI or deployment success")
        pause(min(interval, remaining))


def fetch_runs(sha):
    query = urllib.parse.urlencode({"branch": "mysubapi", "head_sha": sha, "per_page": 100})
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "sub2api-ci-trigger-check",
               "X-GitHub-Api-Version": "2022-11-28", "Cache-Control": "no-cache"}
    token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(f"https://api.github.com/repos/{REPOSITORY}/actions/runs?{query}", headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        raise APIError(f"GitHub API returned HTTP {error.code}; inspect authentication or API rate limits") from None
    except (urllib.error.URLError, TimeoutError, OSError, ValueError):
        raise APIError("GitHub API could not be read; no trigger conclusion was made") from None


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sha", help="exact 40-character commit SHA; default: local HEAD")
    parser.add_argument("--timeout", type=int, default=180, help="maximum polling window in seconds")
    parser.add_argument("--interval", type=int, default=15, help="polling interval in seconds")
    parser.add_argument("--completed", action="store_true", help="wait for a completed successful workflow")
    args = parser.parse_args(argv)
    if args.timeout < 0 or args.interval < 1:
        parser.error("timeout must be nonnegative and interval must be positive")
    sha = args.sha
    if sha is None:
        try:
            sha = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, check=True,
                                 capture_output=True, text=True, timeout=5,
                                 env={**os.environ, "GIT_OPTIONAL_LOCKS": "0"}).stdout.strip()
        except (OSError, subprocess.SubprocessError):
            parser.error("cannot determine local HEAD; supply --sha")
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        parser.error("sha must be a full lowercase 40-character commit identity")
    try:
        run = wait_for_run(lambda: fetch_runs(sha), sha, args.timeout, args.interval, args.completed)
    except (NoRun, RunPending) as error:
        print(str(error), file=sys.stderr)
        return 2
    except APIError as error:
        print(str(error), file=sys.stderr)
        return 3
    print(json.dumps({"sha": sha, "run_id": run["id"], "event": run["event"],
                      "status": run.get("status"), "conclusion": run.get("conclusion"),
                      "url": f"https://github.com/{REPOSITORY}/actions/runs/{run['id']}"}, sort_keys=True))
    return int(args.completed and run.get("conclusion") != "success")


if __name__ == "__main__":
    sys.exit(main())
