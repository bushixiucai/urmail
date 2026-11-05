#!/usr/bin/env python3
"""Audit every release tag against its CI evidence (compensating control OPS-15).

GitHub Free private repositories cannot use tag protection rulesets (user
decision 2026-11-06 defers the plan upgrade), so release gating is enforced
after the fact by this audit:

1. List every git tag (and its release, when present).
2. For each tag SHA, require a successful CI run on exactly that SHA.
3. When no successful CI run exists, allow only a documented CI_WAIVED
   release-lock.json whose source_commit matches the tag AND which records a
   non-empty approver. A waiver without an approver is a violation.
4. Print a per-tag verdict table plus a violations list; exit non-zero when
   any violation exists.

Usage: scripts/audit-release-ci.py [--repo OWNER/NAME]
Requires: git (repo checkout), gh CLI authenticated, python3.
"""

import argparse
import json
import subprocess
import sys
from pathlib import Path

CI_WAIVED = "CI_WAIVED"
CI_OK = "CI_OK"
CI_MISSING = "CI_MISSING"

REPO_ROOT = Path(__file__).resolve().parent.parent
LOCK_GLOBS = ("tmp/*/release-lock.json", "tmp/*/*/release-lock.json")


def run(command, **kwargs):
    result = subprocess.run(command, capture_output=True, text=True, **kwargs)
    if result.returncode != 0:
        raise RuntimeError(f"{command[0]} failed: {result.stderr.strip()}")
    return result.stdout


def list_tags():
    """Return (tag, sha) pairs, oldest first, from the local checkout."""
    lines = run(["git", "-C", str(REPO_ROOT), "for-each-ref", "refs/tags",
                 "--format=%(refname:short) %(objecttype) %(objectname)"])
    tags = []
    for line in lines.splitlines():
        parts = line.split()
        if len(parts) != 3:
            continue
        tag, objtype, objid = parts
        # Annotated tags point at a tag object; resolve to the commit.
        sha = objid if objtype == "commit" else run(
            ["git", "-C", str(REPO_ROOT), "rev-parse", f"{tag}^{{commit}}"]).strip()
        tags.append((tag, sha))
    return sorted(tags, key=lambda item: item[0])


def successful_runs(repo, sha):
    """Return the list of successful CI run ids for an exact head SHA."""
    output = run(["gh", "api",
                  f"repos/{repo}/actions/runs?head_sha={sha}&per_page=100"])
    runs = []
    try:
        payload = json.loads(output)
    except json.JSONDecodeError:
        return runs
    for run_item in payload.get("workflow_runs", []):
        if run_item.get("conclusion") == "success":
            runs.append((run_item.get("id"), run_item.get("name")))
    return runs


def load_waived_locks():
    """Map release SHA -> release-lock.json body for CI_WAIVED releases."""
    locks = {}
    for pattern in LOCK_GLOBS:
        for path in sorted(REPO_ROOT.glob(pattern)):
            try:
                body = json.loads(path.read_text())
            except (OSError, json.JSONDecodeError):
                continue
            if body.get("ci_status") == CI_WAIVED and body.get("source_commit"):
                locks.setdefault(body["source_commit"], (path, body))
    return locks


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default="bushixiucai/urmail")
    args = parser.parse_args()

    tags = list_tags()
    waived_locks = load_waived_locks()
    rows = []
    violations = []
    for tag, sha in tags:
        runs = successful_runs(args.repo, sha)
        if runs:
            verdict = CI_OK
            detail = f"run {runs[0][0]} ({runs[0][1]})"
        elif sha in waived_locks:
            path, body = waived_locks[sha]
            approver = body.get("approver")
            if approver:
                verdict = CI_WAIVED
                detail = f"approver={approver} run={body.get('ci_run_id')} lock={path.name}"
            else:
                verdict = "CI_WAIVED_NO_APPROVER"
                detail = f"waiver without approver: {path.relative_to(REPO_ROOT)}"
                violations.append(
                    f"{tag} ({sha[:12]}): CI_WAIVED release lock records no approver ({path})")
        else:
            verdict = CI_MISSING
            detail = "no successful CI run and no CI_WAIVED release lock"
            violations.append(
                f"{tag} ({sha[:12]}): no successful CI run for the release SHA and no documented waiver")
        rows.append((tag, sha[:12], verdict, detail))

    print(f"audit-release-ci: repo={args.repo} tags={len(tags)}")
    print(f"{'tag':<32} {'sha':<14} {'verdict':<22} detail")
    for tag, sha, verdict, detail in rows:
        print(f"{tag:<32} {sha:<14} {verdict:<22} {detail}")

    if violations:
        print("\nVIOLATIONS:")
        for item in violations:
            print(f"  - {item}")
        return 1
    print("\nno violations")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except RuntimeError as error:
        print(f"audit-release-ci: {error}", file=sys.stderr)
        sys.exit(2)
