#!/usr/bin/env python3
"""Verify isolated PostgreSQL and SQLite sqlc outputs without overwriting either.

The PostgreSQL source snapshot and SQLite-rewritten query set intentionally have
separate generated packages. This checker redirects each config to a private
scratch output, compares that output with its checked-in package, then compiles
both checked-in packages.
"""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
PG_CONFIG = ROOT / "sqlc.postgres.yaml"
SQLITE_CONFIG = ROOT / "sqlc.yaml"
PG_OUTPUT = ROOT / "internal/store/generated/postgres"
SQLITE_OUTPUT = ROOT / "internal/store/generated/sqlite"
TEMP_DIR = ROOT / ".tmp/sqlc_b1_check"
TEMP_CONFIGS = [ROOT / ".sqlc-b1-check-postgres.yaml", ROOT / ".sqlc-b1-check-sqlite.yaml"]


def ensure_disjoint_outputs() -> None:
    pg = PG_OUTPUT.resolve()
    sqlite = SQLITE_OUTPUT.resolve()
    if pg == sqlite or pg in sqlite.parents or sqlite in pg.parents:
        raise SystemExit(f"sqlc outputs overlap: {pg} and {sqlite}")


def rewrite_output(source: Path, target: Path, out: Path) -> None:
    text = source.read_text()
    old = 'out: "internal/store/generated/'
    if text.count(old) != 1:
        raise SystemExit(f"{source.name}: expected one generated output entry")
    rel = os.path.relpath(out, ROOT)
    text = text.replace(old + ('postgres"' if "postgres" in source.name else 'sqlite"'),
                        f'out: "{rel}"', 1)
    target.write_text(text)


def files_under(root: Path) -> dict[str, bytes]:
    if not root.is_dir():
        return {}
    return {p.relative_to(root).as_posix(): p.read_bytes() for p in root.rglob("*") if p.is_file()}


def compare(label: str, expected: Path, actual: Path) -> bool:
    want, got = files_under(expected), files_under(actual)
    keys = sorted(want.keys() | got.keys())
    drift = [k for k in keys if want.get(k) != got.get(k)]
    if drift:
        print(f"{label} sqlc drift: {', '.join(drift[:20])}", file=sys.stderr)
        return False
    print(f"{label} sqlc output up to date ({len(got)} files)")
    return True


def verify_driver_contracts() -> None:
    pg_auth = (PG_OUTPUT / "auth.sql.go").read_text()
    sqlite_auth = (SQLITE_OUTPUT / "auth.sql.go").read_text()
    pg_models = (PG_OUTPUT / "models.go").read_text()
    sqlite_models = (SQLITE_OUTPUT / "models.go").read_text()
    required = {
        "PostgreSQL GetUserRoleForTenant placeholder": "WHERE r.user_id = $1 AND r.tenant_id = $2" in pg_auth,
        "SQLite GetUserRoleForTenant placeholder": "WHERE r.user_id = ?1 AND r.tenant_id = ?2" in sqlite_auth,
        "PostgreSQL session clock": "WHERE expires_at <= now()" in pg_auth,
        "SQLite session clock": "strftime('%s','now')" in sqlite_auth,
        "PostgreSQL DNS timestamp type": "UpdatedAt         pgtype.Timestamptz" in pg_models,
        "SQLite DNS timestamp type": "UpdatedAt         int64" in sqlite_models,
    }
    failed = [name for name, ok in required.items() if not ok]
    if failed:
        raise SystemExit("driver contract mismatch: " + ", ".join(failed))
    print("driver contract ok: placeholders, session clocks, DNS timestamp types are engine-specific")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sqlc", default=None)
    args = ap.parse_args()
    sqlc = args.sqlc or shutil.which("sqlc") or str(Path.home() / "go/bin/sqlc")
    if not Path(sqlc).is_file():
        print("sqlc executable not found", file=sys.stderr)
        return 2
    ensure_disjoint_outputs()
    if TEMP_DIR.exists() or any(p.exists() for p in TEMP_CONFIGS):
        print("refusing to overwrite an existing B1 sqlc scratch path", file=sys.stderr)
        return 2
    TEMP_DIR.mkdir(parents=True)
    try:
        configs = [
            (PG_CONFIG, TEMP_CONFIGS[0], TEMP_DIR / "postgres", PG_OUTPUT, "PostgreSQL"),
            (SQLITE_CONFIG, TEMP_CONFIGS[1], TEMP_DIR / "sqlite", SQLITE_OUTPUT, "SQLite"),
        ]
        for source, target, scratch_out, _, _ in configs:
            rewrite_output(source, target, scratch_out)
            proc = subprocess.run([sqlc, "generate", "-f", str(target.relative_to(ROOT))],
                                  cwd=ROOT, text=True, capture_output=True)
            if proc.returncode != 0:
                sys.stderr.write(proc.stdout)
                sys.stderr.write(proc.stderr)
                return proc.returncode
        ok = all(compare(label, expected, scratch) for _, _, scratch, expected, label in configs)
        if not ok:
            return 1
        verify_driver_contracts()
        proc = subprocess.run(["go", "test", "./internal/store/generated/postgres",
                               "./internal/store/generated/sqlite"], cwd=ROOT, text=True)
        return proc.returncode
    finally:
        for path in TEMP_CONFIGS:
            path.unlink(missing_ok=True)
        shutil.rmtree(TEMP_DIR, ignore_errors=False)


if __name__ == "__main__":
    raise SystemExit(main())
