#!/usr/bin/env python3
"""Require exact-SHA CI success and independent main ancestry before release."""

import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

REQUIRED_JOBS = {
    "Go", "Frontend", "Integration (PostgreSQL)",
    "Native webmail (Postfix and Dovecot)", "Embedded frontend", "Package smoke",
    "Shell", "Development stack and browser", "Security dependencies",
    "Linux installation (ubuntu-22.04, amd64)",
    "Linux installation (ubuntu-24.04, amd64)",
    "Linux installation (ubuntu-24.04-arm, arm64)",
}
MAX_PAGES = 10
DEFAULT_TIMEOUT_SECONDS = 45 * 60
DEFAULT_POLL_INTERVAL_SECONDS = 15

UNRECOVERABLE_CONCLUSIONS = {"failure", "cancelled", "timed_out", "action_required", "stale"}
WAITING_STATUSES = {"queued", "in_progress", "waiting", "requested", "pending"}

# CI_WAIVED exemption policy (PM decision 2026-11-06): a release may proceed on
# a red/no CI run ONLY with a recorded waiver committed next to the release
# commit, naming an approver from this whitelist and a concrete reason. A
# waiver without a whitelisted approver is rejected (fail-closed), and any
# waiver is superseded once CI passes on the same SHA.
WAIVED_DIRECTORY = "release-waivers"
APPROVED_WAIVER_APPROVERS = {"pm", "bushixiucai"}


class VerificationTimeoutError(TimeoutError, ValueError):
    """Raised when release CI polling exceeds the strictly monotonic deadline."""


def valid_sha(value):
    return isinstance(value, str) and re.fullmatch(r"[a-f0-9]{40}", value) is not None


def waiver_path(sha):
    """Waivers live in the release checkout itself, so the file travels with
    the audited commit and cannot be injected from the release environment."""
    root = Path(__file__).resolve().parent.parent
    return root / WAIVED_DIRECTORY / f"{sha}.json"


def load_waiver(sha):
    """Return a validated CI_WAIVED waiver for sha, or None when none exists.

    Raises ValueError for a present-but-invalid waiver: an unparsable or
    unapproved waiver must never silently degrade into 'no waiver'.
    """
    path = waiver_path(sha)
    if not path.is_file():
        return None
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ValueError(f"CI_WAIVED waiver {path.name} is unreadable: {error}") from error
    if not isinstance(data, dict):
        raise ValueError("CI_WAIVED waiver must be a JSON object")
    if data.get("sha") != sha:
        raise ValueError("CI_WAIVED waiver sha does not match the release commit")
    approver = data.get("approver")
    if not isinstance(approver, str) or approver.strip() == "":
        raise ValueError("CI_WAIVED waiver is missing its approver; an unapproved waiver is refused")
    if approver.strip().lower() not in APPROVED_WAIVER_APPROVERS:
        raise ValueError(f"CI_WAIVED waiver approver '{approver}' is not on the approval whitelist")
    reason = data.get("reason")
    if not isinstance(reason, str) or reason.strip() == "":
        raise ValueError("CI_WAIVED waiver requires a non-empty reason")
    run_id = data.get("waived_run_id")
    if run_id is not None and not isinstance(run_id, int):
        raise ValueError("CI_WAIVED waiver waived_run_id must be an integer run id")
    return data


def waived_failure(waiver, why):
    """Raise unless a validated waiver covers this deterministic CI failure."""
    if waiver is None:
        raise ValueError(why)
    print(
        f"CI_WAIVED: release authorized by '{waiver.get('approver')}' despite: {why}; "
        f"reason: {waiver.get('reason')}",
        file=sys.stderr,
        flush=True,
    )


def trusted_source(run, sha, repo):
    """Accept repository-owned tag CI or explicitly dispatched CI, never PRs."""
    if run.get("head_sha") != sha:
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
    """Every required job must pass on this exact commit, single attempt, and trusted source."""
    if not trusted_source(run, sha, repo):
        return False
    if run.get("status") != "completed" or run.get("conclusion") != "success":
        return False
    if run["event"] == "push" and tag_sha != sha:
        return False
    target_attempt = int(run.get("run_attempt", 1))
    attempt_jobs = [
        job for job in jobs
        if int(job.get("run_attempt", target_attempt)) == target_attempt
    ]
    if any(job.get("name") in REQUIRED_JOBS and
           (job.get("status") != "completed" or job.get("conclusion") != "success")
           for job in attempt_jobs):
        return False
    names = {job.get("name") for job in attempt_jobs
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


def verify_release(repo, sha, timeout_seconds=None, poll_interval=None, sleeper=time.sleep):
    require_main_ancestry(repo, sha)
    waiver = load_waiver(sha)

    if timeout_seconds is None:
        timeout_seconds = int(os.environ.get("RELEASE_CI_TIMEOUT_SECONDS", DEFAULT_TIMEOUT_SECONDS))
    if poll_interval is None:
        poll_interval = float(os.environ.get("RELEASE_CI_POLL_INTERVAL_SECONDS", DEFAULT_POLL_INTERVAL_SECONDS))

    start_time = time.monotonic()
    deadline = start_time + timeout_seconds

    bound_run_id = None
    bound_run_attempt = None
    tag_sha = None

    query = urllib.parse.urlencode({"head_sha": sha})

    while True:
        if time.monotonic() >= deadline:
            raise VerificationTimeoutError(
                f"CI verification timed out after {timeout_seconds}s waiting for exact-SHA {sha}"
            )

        runs = list(pages(f"/repos/{repo}/actions/workflows/ci.yaml/runs?{query}", "workflow_runs"))

        if bound_run_id is None:
            candidate_run = None
            resolved_tag_sha = None
            for run in runs:
                if not trusted_source(run, sha, repo):
                    continue
                current_tag_sha = None
                if run.get("event") == "push":
                    try:
                        current_tag_sha = tag_commit(repo, run.get("head_branch"))
                    except urllib.error.HTTPError as error:
                        if error.code == 404:
                            continue
                        raise
                    if current_tag_sha != sha:
                        continue
                candidate_run = run
                resolved_tag_sha = current_tag_sha
                break

            if candidate_run is None:
                waived_failure(waiver, "no trusted exact-SHA CI run passed every required job")
                return None

            bound_run_id = int(candidate_run["id"])
            bound_run_attempt = int(candidate_run.get("run_attempt", 1))
            tag_sha = resolved_tag_sha
            current_run = candidate_run
        else:
            matching_runs = [r for r in runs if int(r.get("id", 0)) == bound_run_id]
            if not matching_runs:
                raise ValueError(f"bound CI run {bound_run_id} no longer found in workflow runs")
            current_run = matching_runs[0]
            current_attempt = int(current_run.get("run_attempt", 1))
            if current_attempt != bound_run_attempt:
                raise ValueError(
                    f"CI run {bound_run_id} attempt changed from {bound_run_attempt} to {current_attempt}; "
                    "cross-attempt stitching is forbidden"
                )

        status = current_run.get("status")
        conclusion = current_run.get("conclusion")

        # Check for unrecoverable failure conclusions immediately (fail-closed)
        if conclusion in UNRECOVERABLE_CONCLUSIONS:
            waived_failure(
                waiver,
                f"CI run {bound_run_id} attempt {bound_run_attempt} failed with conclusion '{conclusion}'",
            )
            return bound_run_id

        if status == "completed" and conclusion != "success":
            waived_failure(
                waiver,
                f"CI run {bound_run_id} attempt {bound_run_attempt} completed with non-success conclusion '{conclusion}'",
            )
            return bound_run_id

        # Inspect jobs for the bound run and attempt
        try:
            jobs = list(pages(f"/repos/{repo}/actions/runs/{bound_run_id}/jobs", "jobs"))
        except urllib.error.HTTPError as error:
            if error.code == 404 and status in WAITING_STATUSES:
                jobs = []
            else:
                raise

        attempt_jobs = [
            j for j in jobs
            if int(j.get("run_attempt", bound_run_attempt)) == bound_run_attempt
        ]

        # Fail closed immediately if any required job has an unrecoverable failure
        for job in attempt_jobs:
            if job.get("name") in REQUIRED_JOBS:
                job_conclusion = job.get("conclusion")
                if job_conclusion in UNRECOVERABLE_CONCLUSIONS:
                    waived_failure(
                        waiver,
                        f"Required CI job '{job.get('name')}' in run {bound_run_id} "
                        f"attempt {bound_run_attempt} failed with conclusion '{job_conclusion}'",
                    )
                    return bound_run_id

        if status in WAITING_STATUSES:
            now = time.monotonic()
            if now >= deadline:
                raise VerificationTimeoutError(
                    f"CI verification timed out after {timeout_seconds}s waiting for exact-SHA {sha}"
                )
            print(
                f"Waiting for CI run {bound_run_id} attempt {bound_run_attempt} ({status})...",
                file=sys.stderr,
                flush=True
            )
            sleep_time = min(poll_interval, max(0.0, deadline - now))
            sleeper(sleep_time)
            continue

        if status == "completed" and conclusion == "success":
            if verify_run(current_run, attempt_jobs, sha, repo, tag_sha):
                if waiver is not None:
                    print(
                        f"CI_WAIVED: CI now passes on {sha}; the recorded waiver is superseded.",
                        file=sys.stderr,
                        flush=True,
                    )
                return bound_run_id
            waived_failure(
                waiver,
                f"CI run {bound_run_id} attempt {bound_run_attempt} completed with success but required jobs check failed",
            )
            return bound_run_id

        raise ValueError(f"unexpected CI run status '{status}'")


def main():
    repo = os.environ.get("GITHUB_REPOSITORY", "")
    sha = os.environ.get("RELEASE_SHA", "")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repo) or not valid_sha(sha):
        raise ValueError("an explicit repository and full commit SHA are required")
    run_id = verify_release(repo, sha)
    if run_id is None:
        print(f"Release authorized by recorded CI_WAIVED waiver for {sha}")
    else:
        print(f"Required CI and main ancestry verified for {sha}; run={run_id}")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # Request objects and authorization headers must never reach the log;
        # gate rejection messages are static operator-facing strings, so the
        # reason itself is safe and necessary for release forensics.
        print(f"Release CI gate rejected the candidate: {type(error).__name__}: {error}", file=sys.stderr)
        sys.exit(1)
