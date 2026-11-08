#!/usr/bin/env python3
"""Split the Go test packages into disjoint integration shards.

The PostgreSQL integration job used to run every package serially
(`go test -race -p 1 ./internal/... ./cmd/...`), which took 28-32 minutes
because the heaviest package alone needs eight minutes.  The suites are
independent: every fixture creates its own throwaway database (see
internal/testdb), so the packages can be split across parallel jobs, each
with its own PostgreSQL container.

This script owns the split:

  * `--shard NAME` prints the package list of one shard, one per line;
  * `--check` asserts that the shards cover every package reported by
    `go list ./internal/... ./cmd/...` exactly once - no gap, no overlap.

The package times below are a snapshot of the serial integration run of
2026-11-08 (main @ 82fdf98a).  They are only used to report the expected
duration of each shard; the split itself is decided by path rules and does
not depend on them.
"""

from __future__ import annotations

import argparse
import subprocess
import sys

MODULE = "github.com/bushixiucai/urmail"

# Ordered rules; the first matching rule owns the package.  A rule matches a
# package when the paths are equal or when the package lives below the rule
# (so "internal/mail" owns "internal/mail/dovecot").  Packages that match no
# rule fall into the "core" shard, which is the catch-all.
SHARD_RULES: list[tuple[str, list[str]]] = [
    ("handlers", ["internal/api/handlers"]),
    ("data", [
        "internal/db",
        "internal/store",
        "internal/testdb",
        "internal/backupauto",
        "internal/telemetry",
        "internal/archive",
    ]),
    ("platform", [
        "internal/loadbench",
        "internal/eas",
        "internal/agentd",
        "internal/marketing",
        "internal/miabimport",
        "cmd/urmailctl",
    ]),
]
CATCH_ALL = "core"

# Serial test time per package, in seconds, measured on 2026-11-08.
MEASURED_SECONDS = {
    "internal/api/handlers": 475.020,
    "internal/db": 223.060,
    "internal/loadbench": 100.837,
    "internal/eas": 90.477,
    "cmd/urmailctl": 88.373,
    "internal/marketing": 80.190,
    "internal/agentd": 70.510,
    "internal/api/middleware": 62.608,
    "internal/testdb": 42.441,
    "internal/api": 39.540,
    "internal/backupauto": 33.834,
    "internal/miabimport": 30.696,
    "internal/telemetry": 29.873,
    "internal/mailboxschedule": 29.740,
    "internal/notify": 29.633,
    "internal/mailboxsend": 26.679,
    "internal/archive": 26.084,
    "internal/mailspam": 23.330,
    "internal/caldav": 23.303,
    "internal/onboarding": 17.600,
    "internal/carddav": 17.000,
    "internal/aliassenders": 13.600,
    "internal/webmail": 13.500,
    "internal/airetrieval": 10.500,
    "internal/brandhost": 7.400,
    "internal/miadbinventory": 6.700,
    "internal/certs": 2.900,
    "internal/domain/mail": 2.800,
    "internal/reconcile": 2.400,
    "cmd/urmaild": 2.000,
    "internal/domain/panelpassword": 1.900,
    "internal/maildnsreport": 1.400,
    "internal/maintenance": 1.300,
    "internal/i18n": 1.300,
    "internal/domain/compliance": 1.100,
    "internal/mail/sievegate": 1.100,
    "internal/mailimport": 1.100,
    "internal/domain/marketingreport": 1.100,
    "internal/ai": 1.100,
    "internal/mail/policy": 1.100,
}


def shard_of(package: str) -> str:
    """Return the shard that owns package (repository relative path)."""
    for name, rules in SHARD_RULES:
        for rule in rules:
            if package == rule or package.startswith(rule + "/"):
                return name
    return CATCH_ALL


def all_shards() -> list[str]:
    names = [name for name, _ in SHARD_RULES]
    names.append(CATCH_ALL)
    return names


def list_packages() -> list[str]:
    """Return every Go package of the repository, relative to the module."""
    result = subprocess.run(
        ["go", "list", "./internal/...", "./cmd/..."],
        check=True,
        capture_output=True,
        text=True,
    )
    packages = []
    for line in result.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        if line.startswith(MODULE + "/"):
            line = line[len(MODULE) + 1:]
        packages.append(line)
    return sorted(packages)


def assign(packages: list[str]) -> dict[str, list[str]]:
    shards = {name: [] for name in all_shards()}
    for package in packages:
        shards[shard_of(package)].append(package)
    return shards


def report(shards: dict[str, list[str]], packages: list[str]) -> None:
    """Print the shard table and the measured duration estimate."""
    print("integration shards:")
    for name in all_shards():
        members = shards[name]
        seconds = sum(MEASURED_SECONDS.get(p, 0.0) for p in members)
        known = sum(1 for p in members if p in MEASURED_SECONDS)
        print(
            f"  {name:<9} {len(members):>3} packages"
            f"  measured {seconds / 60:5.1f} min"
            f"  ({known}/{len(members)} packages with a recorded time)"
        )
    total = sum(len(v) for v in shards.values())
    print(f"  {'total':<9} {total:>3} packages")


def check(packages: list[str]) -> int:
    shards = assign(packages)
    report(shards, packages)

    problems: list[str] = []
    covered: list[str] = []
    for name in all_shards():
        if not shards[name]:
            problems.append(f"shard {name} is empty")
        covered.extend(shards[name])

    duplicates = sorted({p for p in covered if covered.count(p) > 1})
    if duplicates:
        problems.append(f"packages claimed by more than one shard: {duplicates}")

    missing = sorted(set(packages) - set(covered))
    if missing:
        problems.append(f"packages missing from every shard: {missing}")

    extra = sorted(set(covered) - set(packages))
    if extra:
        problems.append(f"shards claim unknown packages: {extra}")

    if problems:
        for problem in problems:
            print(f"FAIL: {problem}", file=sys.stderr)
        return 1

    slowest = max(
        (sum(MEASURED_SECONDS.get(p, 0.0) for p in members), name)
        for name, members in shards.items()
    )
    print(
        f"coverage OK: {len(packages)} packages, no gap, no overlap"
        f" (slowest shard {slowest[1]} at {slowest[0] / 60:.1f} min)"
    )
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--shard",
        action="append",
        help="print the packages of one shard (repeatable, default: all)",
    )
    parser.add_argument(
        "--check",
        action="store_true",
        help="assert that the shards cover every package exactly once",
    )
    args = parser.parse_args()

    packages = list_packages()
    shards = assign(packages)

    if args.check:
        return check(packages)

    wanted = args.shard if args.shard else all_shards()
    for name in wanted:
        if name not in shards:
            print(f"unknown shard: {name}", file=sys.stderr)
            return 2
        for package in shards[name]:
            # The "./" prefix keeps the path relative to the module, which is
            # what `go test` accepts.
            print(f"./{package}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
