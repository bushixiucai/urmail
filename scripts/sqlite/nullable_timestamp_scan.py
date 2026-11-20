#!/usr/bin/env python3
"""Gate: nullable-timestamp projections must read coalesce(col, 0).

Why: B5b made every timestamp an int64 unix-micro value (0 = zero value), but
the SQLite schema keeps nullable timestamp columns as NULL because triggers
depend on `IS NULL` / `IS NOT NULL`. Scanning SQL NULL into int64 fails, so a
projection of a nullable timestamp must be wrapped in `coalesce(col, 0)`.

A projection counts as compliant only when its expression is exactly
`coalesce(col, 0)` / `coalesce(t.col, 0)` (the wrapper produced by
scripts/sqlite/wrap_nullable_ts_projections.py). Anything else — a bare
column reference, an aggregate, a CASE — can surface SQL NULL to the int64
scan target and is reported as a violation. `--check` exits 1 while any
violation remains; it runs in CI next to the other sqlc drift checks.

Usage: python3 scripts/sqlite/nullable_timestamp_scan.py [--check]
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCHEMA = ROOT / "migrations_sqlite" / "0001_init.sql"
SQLC = ROOT / "sqlc.yaml"

TIMESTAMP = re.compile(r'(at|until|since|time)$')
COALESCED = re.compile(r'^coalesce\s*\(\s*([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)?)\s*,\s*0\s*\)$', re.I)


def nullable_timestamps():
    cols = {}
    for table in re.finditer(r'CREATE TABLE (?:IF NOT EXISTS )?"?(\w+)"? \((.*?)\n\);',
                             SCHEMA.read_text(), re.S):
        for line in table.group(2).split("\n"):
            m = re.match(r'"(\w+)"\s+INTEGER\s*(.*)', line.strip())
            if m and "NOT NULL" not in m.group(2) and TIMESTAMP.search(m.group(1)):
                cols.setdefault(m.group(1), []).append(table.group(1))
    return cols


def query_files():
    out = []
    for line in SQLC.read_text().split("\n"):
        m = re.search(r'queries/([^"/]+)\.sql', line)
        if m:
            out.append(m.group(1))
    return out


def split_top(text, sep=","):
    out, depth, cur = [], 0, ""
    for ch in text:
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
        if ch == sep and depth == 0:
            out.append(cur.strip())
            cur = ""
        else:
            cur += ch
    if cur.strip():
        out.append(cur.strip())
    return out


def projections(sql):
    body = " ".join(sql.split())
    m = re.search(r"\bRETURNING\b(.*)$", body)
    if m:
        return split_top(m.group(1))
    m = re.search(r"\bSELECT\b(.*?)\bFROM\b", body)
    if m:
        return split_top(m.group(1))
    return []


def main():
    check = "--check" in sys.argv[1:]
    nullcols = nullable_timestamps()
    pairs, ok_count = {}, 0
    for name in query_files():
        path = ROOT / "internal" / "store" / f"{name}.sql.go"
        if not path.exists():
            continue
        text = path.read_text()
        for decl in re.finditer(r'const (\w+) = `([^`]*)`', text):
            method, sql = decl.group(1), decl.group(2)
            if re.search(r"\b(SELECT|RETURNING) \*", sql):
                continue
            projs = projections(sql)
            rest = text[text.find(f"const {method} = `"):]
            scan = re.search(r"\.Scan\((.*?)\)\n", rest, re.S)
            if not scan:
                continue
            fields = [a or c for a, b, c in
                      re.findall(r"&i\.(\w+)|&(\w+)\.(\w+)", scan.group(1))]
            if not fields or len(fields) != len(projs):
                continue
            for field, expr in zip(fields, projs):
                value = expr.strip()
                # split "expr AS alias" at the final alias boundary
                m = re.match(r"^(.*?)\s+AS\s+([A-Za-z_]\w*)$", value, re.I | re.S)
                body, base = (m.group(1), m.group(2)) if m else (value, value)
                base = base.split(".")[-1].strip()
                if base not in nullcols:
                    continue
                cm = COALESCED.match(body.strip())
                if cm and cm.group(1).split(".")[-1].lower() == base.lower():
                    ok_count += 1
                    continue
                pairs.setdefault(f"{base} ({','.join(nullcols[base])})",
                                 set()).add(method)
    total = sum(len(v) for v in pairs.values())
    if check:
        if total:
            print(f"FAIL: {len(pairs)} nullable timestamp columns bare-projected "
                  f"by {total} statements ({ok_count} compliant)", file=sys.stderr)
            for col in sorted(pairs):
                print(f"  {col}: {', '.join(sorted(pairs[col]))}", file=sys.stderr)
            return 1
        print(f"nullable-timestamp projections: all wrapped "
              f"({ok_count} coalesce projections, 0 violations)")
        return 0
    print(f"{len(pairs)} nullable timestamp columns with unwrapped projections "
          f"({total} statements); {ok_count} projections compliant")
    for col in sorted(pairs):
        print(f"  {col}: {', '.join(sorted(pairs[col]))}")
    return 0


if __name__ == "__main__":
    sys.exit(main())