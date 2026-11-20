#!/usr/bin/env python3
"""List generated queries that project a nullable timestamp column bare.

Why: B5b made every timestamp an int64 unix-micro value (0 = zero value), but
the SQLite schema keeps nullable timestamp columns as NULL because triggers
depend on `IS NULL` / `IS NOT NULL`. Scanning SQL NULL into int64 fails, so a
projection of a nullable timestamp must be wrapped in `coalesce(col, 0)` (or
the Phase 2 DBTX adapter must translate NULL to 0).

This is an inventory, not a gate: it prints the statement/column pairs that
still project bare, so the Phase 2 wiring can drive the count to zero and a
CI ratchet can be added there.

Usage: python3 scripts/sqlite/nullable_timestamp_scan.py
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCHEMA = ROOT / "migrations_sqlite" / "0001_init.sql"
SQLC = ROOT / "sqlc.yaml"

TIMESTAMP = re.compile(r'(at|until|since|time)$')


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
    nullcols = nullable_timestamps()
    pairs = {}
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
                if re.search(r"\bas\b", value, re.I):
                    value = re.split(r"\bas\b", value, flags=re.I)[-1].strip()
                base = value.split(".")[-1].strip()
                if base in nullcols:
                    pairs.setdefault(f"{base} ({','.join(nullcols[base])})", set()).add(method)
    total = sum(len(v) for v in pairs.values())
    print(f"{len(pairs)} nullable timestamp columns bare-projected by {total} statements")
    for col in sorted(pairs):
        print(f"  {col}: {', '.join(sorted(pairs[col]))}")
    return 0


if __name__ == "__main__":
    sys.exit(main())