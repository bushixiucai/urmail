#!/usr/bin/env python3
"""check_store_boundary.py — S2/S3 post-sqlc store.New boundary & isolation sync.

Enforces three invariants from v3.2 §2.1:
  1. internal/db/isolation/method_scopes.json stays byte-identical to
     scripts/sqlite/method_scopes.json.
  2. Non-test Go code outside internal/db/store.go has zero direct `store.New(`
     calls (all production callers assemble through db.Queries(tx)).
  3. internal/store/db.go preserves the production boundary comment on func New
     even after `sqlc generate` regenerates db.go.

Usage:
  python3 scripts/sqlite/check_store_boundary.py          # sync isolation json & annotate db.go, then verify
  python3 scripts/sqlite/check_store_boundary.py --check  # read-only verification (for make sqlc-check / CI)
"""
import argparse
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
SRC_SCOPES = ROOT / "scripts" / "sqlite" / "method_scopes.json"
DST_SCOPES = ROOT / "internal" / "db" / "isolation" / "method_scopes.json"
STORE_DB_GO = ROOT / "internal" / "store" / "db.go"
ALLOWED_STORE_NEW_FILE = "internal/db/store.go"

BOUNDARY_COMMENT = (
    "// New constructs Queries. Production callers must use db.Queries\n"
    "// (internal/db/store.go), the single assembly point (v3.2 §2.1).\n"
    "func New(db DBTX) *Queries {"
)

STORE_NEW_RE = re.compile(r"\bstore\.New\(")


def iter_non_test_go_files():
    for path in sorted(ROOT.rglob("*.go")):
        rel = path.relative_to(ROOT).as_posix()
        if rel.startswith(".tmp/") or rel.startswith("vendor/"):
            continue
        if rel.endswith("_test.go"):
            continue
        yield rel, path


def check_or_sync_scopes(check_only: bool) -> list[str]:
    errors: list[str] = []
    src_bytes = SRC_SCOPES.read_bytes()
    dst_bytes = DST_SCOPES.read_bytes() if DST_SCOPES.exists() else None
    if dst_bytes != src_bytes:
        if check_only:
            errors.append(
                f"{DST_SCOPES.relative_to(ROOT)} differs from {SRC_SCOPES.relative_to(ROOT)}; "
                "run python3 scripts/sqlite/check_store_boundary.py"
            )
        else:
            DST_SCOPES.parent.mkdir(parents=True, exist_ok=True)
            DST_SCOPES.write_bytes(src_bytes)
    return errors


def check_or_annotate_store_db_go(check_only: bool) -> list[str]:
    errors: list[str] = []
    text = STORE_DB_GO.read_text()
    if BOUNDARY_COMMENT not in text:
        if check_only:
            errors.append(
                f"{STORE_DB_GO.relative_to(ROOT)} missing post-sqlc boundary comment on func New"
            )
        else:
            updated, count = re.subn(
                r"(?m)^func New\(db DBTX\) \*Queries \{",
                BOUNDARY_COMMENT,
                text,
                count=1,
            )
            if count != 1:
                errors.append(f"could not locate func New(db DBTX) *Queries in {STORE_DB_GO}")
            else:
                STORE_DB_GO.write_text(updated)
    return errors


def scan_non_test_store_new() -> tuple[int, list[str]]:
    total = 0
    offenders: list[str] = []
    for rel, path in iter_non_test_go_files():
        for lineno, line in enumerate(path.read_text().splitlines(), start=1):
            code = line.split("//", 1)[0]
            matches = STORE_NEW_RE.findall(code)
            if not matches:
                continue
            total += len(matches)
            if rel != ALLOWED_STORE_NEW_FILE:
                offenders.append(f"{rel}:{lineno}: {line.strip()}")
    return total, offenders


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true")
    args = ap.parse_args()

    errors: list[str] = []
    errors.extend(check_or_sync_scopes(args.check))
    errors.extend(check_or_annotate_store_db_go(args.check))

    total, offenders = scan_non_test_store_new()
    if offenders:
        errors.append(
            f"found {len(offenders)} forbidden non-test store.New( call(s) outside {ALLOWED_STORE_NEW_FILE}:\n  "
            + "\n  ".join(offenders[:20])
        )
    if total != 1:
        errors.append(f"expected non-test store.New( total == 1 (only {ALLOWED_STORE_NEW_FILE}), got {total}")

    if errors:
        for err in errors:
            print(f"store-boundary error: {err}", file=sys.stderr)
        return 1

    print(
        f"store boundary ok (non-test store.New( = {total}, only in {ALLOWED_STORE_NEW_FILE}; "
        "internal/db/isolation/method_scopes.json in sync)"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
