#!/usr/bin/env python3
"""gen_sqlc.py — the B5a sqlc entry point.

Why not plain `sqlc generate`?
  * sqlc's SQLite engine slices statements by byte offset; the repo keeps its
    Chinese notes at end-of-file (the only safe position), so the query set
    itself is fine, but the generated models would also pick up the webmail-v1
    parity tables (snoozes/labels/message_labels + three ALTERed columns) that
    B5a deliberately does not adopt yet.
  * while platform-only files are blocked, only the tenant `*.sql.go` files are
    regenerated; models.go / db.go / querier.go are held at their pre-switch
    shape until the remaining platform files are unblocked.

So: generate into a scratch directory with the same sqlc.yaml, copy back only
the `*.sql.go` files, and — with --check — fail if they differ from the repo.
"""
import argparse
import os
import pathlib
import re
import shutil
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
CFG = ROOT / "sqlc.yaml"
GEN = ROOT / ".tmp" / "sqlc_gen"
STORE = ROOT / "internal" / "store"


def scratch_config():
    """Copy sqlc.yaml under .tmp/ with source paths rebased onto .tmp/.

    sqlc joins schema/queries onto the config file's directory, and the
    scratch config lives in .tmp/, so source paths become `../...` and only
    `out` stays inside .tmp/.
    """
    src = CFG.read_text()
    out = str(GEN.relative_to(ROOT / ".tmp"))
    tmp = ROOT / ".tmp" / "sqlc.yaml"

    def rel_path(m):
        # sqlc joins schema/queries onto the config's directory; the scratch
        # config sits in .tmp/, so emit paths relative to that directory.
        return f'{m.group(1)}"{os.path.relpath(ROOT / m.group(2), tmp.parent)}"'

    src2, n = re.subn(r'(?m)^(    schema: )"([^"]*)"', rel_path, src)
    if n != 1:
        raise SystemExit("sqlc.yaml: expected exactly one schema entry")
    src2, n = re.subn(r'(?m)^(      - )"([^"]*)"', rel_path, src2)
    if n < 1:
        raise SystemExit("sqlc.yaml: expected a queries list")
    src2, n = re.subn(r'(?m)^        out: "[^"]*"', f'        out: "{out}"', src2)
    if n != 1:
        raise SystemExit("sqlc.yaml: expected exactly one go.out entry")
    tmp.parent.mkdir(parents=True, exist_ok=True)
    tmp.write_text(src2)
    return tmp


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true")
    ap.add_argument("--sqlc", default=None)
    args = ap.parse_args()
    sqlc = args.sqlc or shutil.which("sqlc") or str(pathlib.Path.home() / "go" / "bin" / "sqlc")

    cfg = scratch_config()
    if GEN.exists():
        shutil.rmtree(GEN)
    proc = subprocess.run([sqlc, "generate", "-f", str(cfg.relative_to(ROOT))],
                          cwd=ROOT, capture_output=True, text=True)
    if proc.returncode != 0:
        sys.stderr.write(proc.stdout)
        sys.stderr.write(proc.stderr)
        raise SystemExit("sqlc generate failed")

    generated = sorted(GEN.glob("*.sql.go"))
    if not generated:
        raise SystemExit("sqlc produced no *.sql.go files")
    drift = []
    for g in generated:
        dst = STORE / g.name
        new = g.read_text()
        old = dst.read_text() if dst.exists() else None
        if old != new:
            drift.append(g.name)
            if not args.check:
                dst.write_text(new)
    models = GEN / "models.go"
    expected_model_delta = models.exists() and models.read_text() != (STORE / "models.go").read_text()
    if args.check:
        if drift:
            print("sqlc drift (run scripts/sqlite/gen_sqlc.py): " + ", ".join(drift), file=sys.stderr)
            return 1
        print(f"sqlc output up to date ({len(generated)} files)")
        return 0
    print(f"regenerated {len(generated) - len(drift)} files, updated {len(drift)}"
          + ("" if not expected_model_delta else
             "; models.go intentionally held (webmail parity tables not adopted in B5a)"))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
