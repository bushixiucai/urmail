#!/usr/bin/env python3
"""Require exact-SHA CI success and independent main ancestry before release."""

import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

REQUIRED_JOBS = {
    "Go", "Frontend", "Integration (PostgreSQL)",
    "Native webmail (Postfix and Dovecot)", "Embedded frontend", "Package smoke",
    "Shell", "Development stack and browser", "Security dependencies",
    "Linux installation (ubuntu-22.04, amd64)",
    "Linux installation (ubuntu-24.04, amd64)",
    "Linux installation (ubuntu-24.04-arm, arm64)",
}
MAX_PAGES = 10


def valid_sha(value):
    return isinstance(value, str) and re.fullmatch(r"[a-f0-9]{40}", value) is not None


def trusted_source(run, sha, repo):
    """Accept repository-owned tag CI or explicitly dispatched CI, never PRs."""
    if (run.get("head_sha") != sha or run.get("status") != "completed"
            or run.get("conclusion") != "success"):
        return False
    for field in ("repository", "head_repository"):
        name = (run.get(field) or {}).get("full_name", "")
        if not isinstance(name, str) or name.lower() != repo.lower():
            return False
    branch = run.get("head_branch")
    if not isinstance(branch, str) or not branch or any(c.isspace() or ord(c) < 32 for c in branch):
        return False
    return (run.get("event") == "workflow_dispatch" and branch == "main") or (
        run.get("event") == "push" and branch.startswith("v")
    )


def verify_run(run, jobs, sha, repo, tag_sha=None):
    """Every required job must pass on this exact commit and trusted source."""
    if not trusted_source(run, sha, repo):
        return False
    if run["event"] == "push" and tag_sha != sha:
        return False
    if any(job.get("name") in REQUIRED_JOBS and
           (job.get("status") != "completed" or job.get("conclusion") != "success")
           for job in jobs):
        return False
    names = {job.get("name") for job in jobs
             if job.get("status") == "completed" and job.get("conclusion") == "success"}
    return REQUIRED_JOBS <= names


def fetch(path):
    base = os.environ.get("GITHUB_API_URL", "https://api.github.com").rstrip("/")
    if not base.startswith("https://"):
        raise ValueError("GitHub CI verification requires HTTPS")
    request = urllib.request.Request(base + path, headers={
        "Accept": "application/vnd.github+json",
        "Authorization": "Bearer " + os.environ["GH_TOKEN"],
        "X-GitHub-Api-Version": "2022-11-28",
    })
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)


def pages(path, key):
    """Read bounded API pages without treating a truncated first page as complete."""
    separator = "&" if "?" in path else "?"
    for page in range(1, MAX_PAGES + 1):
        data = fetch(f"{path}{separator}per_page=100&page={page}")
        rows = data[key]
        if not isinstance(rows, list) or len(rows) > 100:
            raise ValueError("unexpected CI pagination response")
        yield from rows
        if len(rows) < 100:
            return
    raise ValueError("CI pagination exceeds the bounded verification budget")


def require_main_ancestry(repo, sha):
    """Resolve main independently; a matching branch label is not ancestry proof."""
    current = fetch(f"/repos/{repo}/git/ref/heads/main")["object"]
    main_sha = current.get("sha")
    if current.get("type") != "commit" or not valid_sha(main_sha):
        raise ValueError("main does not resolve to a commit")
    comparison = fetch(f"/repos/{repo}/compare/{sha}...{main_sha}")
    if (comparison.get("status") not in ("identical", "ahead")
            or comparison.get("base_commit", {}).get("sha") != sha
            or comparison.get("merge_base_commit", {}).get("sha") != sha):
        raise ValueError("release commit is not reachable from main")


def tag_commit(repo, name):
    """Resolve lightweight/annotated tags and reject moved or malformed targets."""
    ref = urllib.parse.quote(name, safe="")
    target = fetch(f"/repos/{repo}/git/ref/tags/{ref}")["object"]
    for _ in range(6):
        oid = target.get("sha")
        if not valid_sha(oid):
            raise ValueError("invalid tag object")
        if target.get("type") == "commit":
            return oid
        if target.get("type") != "tag":
            raise ValueError("release tag does not resolve to a commit")
        target = fetch(f"/repos/{repo}/git/tags/{oid}")["object"]
    raise ValueError("annotated tag nesting exceeds the verification budget")


def verify_release(repo, sha):
    require_main_ancestry(repo, sha)
    query = urllib.parse.urlencode({"head_sha": sha, "status": "success"})
    runs = pages(f"/repos/{repo}/actions/workflows/ci.yaml/runs?{query}", "workflow_runs")
    for run in runs:
        if not trusted_source(run, sha, repo):
            continue
        tag_sha = None
        if run["event"] == "push":
            try:
                tag_sha = tag_commit(repo, run["head_branch"])
            except urllib.error.HTTPError as error:
                if error.code == 404:
                    continue
                raise
            if tag_sha != sha:
                continue
        jobs = list(pages(f"/repos/{repo}/actions/runs/{int(run['id'])}/jobs", "jobs"))
        if verify_run(run, jobs, sha, repo, tag_sha):
            return int(run["id"])
    raise ValueError("no trusted exact-SHA CI run passed every required job")


def main():
    repo = os.environ.get("GITHUB_REPOSITORY", "")
    sha = os.environ.get("RELEASE_SHA", "")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repo) or not valid_sha(sha):
        raise ValueError("an explicit repository and full commit SHA are required")
    run_id = verify_release(repo, sha)
    print(f"Required CI and main ancestry verified for {sha}; run={run_id}")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # Request objects and authorization headers must never reach the log.
        print(f"Release CI gate rejected the candidate: {type(error).__name__}", file=sys.stderr)
        sys.exit(1)
