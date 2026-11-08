#!/usr/bin/env python3
"""Classify the files a CI run touches into the job groups of the workflow.

The gatekeeper job of the workflow runs this script and publishes four
booleans (`go`, `frontend`, `deps`, `docs`).  Every other job declares
`needs: changes` plus an `if:` condition built from them, so a pull request
only pays for the jobs its change can actually break.

Classification rules:

  docs      *.md, VERSION, docs/**
  go        internal/**, cmd/**, api/**, *.go, go.mod, go.sum, Makefile,
            packaging/**, installer/**, scripts/**
  frontend  web/**
  deps      go.mod, go.sum, web/package.json, web/package-lock.json

Anything else - `.github/**` for example - is *unknown*, and an unknown path
turns every group on.  The classifier therefore fails safe: a path nobody
thought about can never silently skip a job.  Pushes to main, tag pushes and
manual runs are always treated as a full change.
"""

from __future__ import annotations

import os
import subprocess
import sys

ZERO_SHA = "0" * 40

# Ordered path rules.  The first matching rule wins; a path that matches no
# rule is unknown and triggers the full job set.
DOCS_RULES = ("docs/",)
GO_RULES = (
    "internal/",
    "cmd/",
    "api/",
    "packaging/",
    "installer/",
    "scripts/",
)


def run_git(args: list[str]) -> str:
    result = subprocess.run(
        ["git", *args], capture_output=True, text=True, check=False
    )
    if result.returncode != 0:
        raise RuntimeError(result.stderr.strip() or f"git {' '.join(args)} failed")
    return result.stdout


def commit_exists(sha: str) -> bool:
    try:
        run_git(["cat-file", "-e", f"{sha}^{{commit}}"])
        return True
    except RuntimeError:
        return False


def try_fetch(sha: str) -> bool:
    subprocess.run(
        ["git", "fetch", "--no-tags", "--depth=1", "origin", sha],
        capture_output=True,
        text=True,
        check=False,
    )
    return commit_exists(sha)


def is_docs(path: str) -> bool:
    if path == "VERSION":
        return True
    if path.endswith(".md"):
        return True
    return any(path.startswith(rule) for rule in DOCS_RULES)


def is_go(path: str) -> bool:
    if path.endswith(".go"):
        return True
    if path in ("go.mod", "go.sum", "Makefile", "makefile"):
        return True
    return any(path.startswith(rule) for rule in GO_RULES)


def is_frontend(path: str) -> bool:
    return path.startswith("web/")


def is_deps(path: str) -> bool:
    return path in (
        "go.mod",
        "go.sum",
        "web/package.json",
        "web/package-lock.json",
    )


def full_change_set(reason: str) -> list[str]:
    print(f"running the full job set: {reason}", file=sys.stderr)
    return []


def changed_files() -> tuple[list[str] | None, str]:
    """Return the changed paths, or None when the full set must run."""
    event = os.environ.get("EVENT_NAME", "")
    ref = os.environ.get("REF", "")

    if event == "workflow_dispatch":
        return None, "manual run"
    if event == "push" and (ref == "refs/heads/main" or ref.startswith("refs/tags/")):
        return None, "push to main or a tag"

    if event == "pull_request":
        base = os.environ.get("BASE_SHA", "")
        head = os.environ.get("HEAD_SHA", "")
        if not base or not head:
            return None, "pull request without base or head"
        source = "pull request"
    elif event == "push":
        base = os.environ.get("PUSH_BEFORE", "")
        head = os.environ.get("PUSH_AFTER", "")
        if not base or base == ZERO_SHA:
            base = "HEAD^"
            head = "HEAD"
        source = "push"
    else:
        return None, f"unknown event {event!r}"

    for sha in (base, head):
        if not commit_exists(sha) and not try_fetch(sha):
            return None, f"commit {sha[:12]} is not available locally"

    try:
        output = run_git(["diff", "--name-only", f"{base}...{head}"])
    except RuntimeError as exc:
        return None, f"diff failed: {exc}"
    files = [line.strip() for line in output.splitlines() if line.strip()]
    if not files:
        return None, "no changed file in the diff"
    print(f"{source}: {len(files)} changed file(s)", file=sys.stderr)
    return files, source


def classify(files: list[str]) -> dict[str, bool]:
    result = {"go": False, "frontend": False, "deps": False, "docs": False}
    unknown: list[str] = []
    docs_only = bool(files)

    for path in files:
        if is_docs(path):
            continue
        docs_only = False
        if is_go(path):
            result["go"] = True
            if is_deps(path):
                result["deps"] = True
            continue
        if is_frontend(path):
            result["frontend"] = True
            if is_deps(path):
                result["deps"] = True
            continue
        if is_deps(path):
            result["deps"] = True
            result["go"] = True
            continue
        unknown.append(path)

    if unknown:
        print(
            "unknown path(s), running every job: " + ", ".join(sorted(unknown)),
            file=sys.stderr,
        )
        result = {"go": True, "frontend": True, "deps": True, "docs": False}
        return result

    result["docs"] = docs_only
    return result


def write_outputs(result: dict[str, bool], files: list[str], reason: str) -> None:
    output_path = os.environ.get("GITHUB_OUTPUT")
    if output_path:
        with open(output_path, "a", encoding="utf-8") as handle:
            for key in ("go", "frontend", "deps", "docs"):
                handle.write(f"{key}={'true' if result[key] else 'false'}\n")

    summary_path = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary_path:
        with open(summary_path, "a", encoding="utf-8") as handle:
            handle.write("### CI change detection\n\n")
            handle.write(f"- source: {reason}\n")
            handle.write(f"- changed files: {len(files)}\n")
            for key in ("go", "frontend", "deps", "docs"):
                handle.write(f"- {key}: {str(result[key]).lower()}\n")
            handle.write("\n")


def main() -> int:
    files, reason = changed_files()
    if files is None:
        result = {"go": True, "frontend": True, "deps": True, "docs": False}
        write_outputs(result, [], reason)
        print("go=true frontend=true deps=true docs=false")
        return 0

    result = classify(files)
    write_outputs(result, files, reason)
    print(
        "go={go} frontend={frontend} deps={deps} docs={docs}".format(
            go=str(result["go"]).lower(),
            frontend=str(result["frontend"]).lower(),
            deps=str(result["deps"]).lower(),
            docs=str(result["docs"]).lower(),
        )
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
