#!/usr/bin/env python3
"""Wrap nullable-timestamp projections in coalesce(col, 0) in query sources.

B5b follow-up (2026-11-20): the SQLite schema keeps nullable timestamp
columns as SQL NULL because triggers depend on `IS NULL` / `IS NOT NULL`,
while every Go-side timestamp is an int64 unix-micro value where 0 is the
zero value. Scanning SQL NULL into int64 fails, so every projection of a
nullable timestamp column must read coalesce(col, 0).

This script edits `internal/store/queries/*.sql` (the files listed in
sqlc.yaml) in place:

  1. `SELECT * FROM t` / `RETURNING *` on a single table are expanded into
     the schema column list (order matters: it becomes the scan order),
     with nullable timestamp columns wrapped;
  2. bare column references (`col`, `t.col`) inside explicit SELECT /
     RETURNING projection lists are wrapped in place;
  3. anything it cannot classify (aggregates, CASE, DISTINCT, ...) is left
     untouched and reported — `nullable_timestamp_scan.py --check` fails
     until those are resolved by hand.

Idempotent: already-wrapped projections and non-timestamp columns are left
alone, so a second run is a no-op. Run `python3 scripts/sqlite/gen_sqlc.py`
afterwards to regenerate `*.sql.go`.

Usage:
    python3 scripts/sqlite/wrap_nullable_ts_projections.py [--dry-run]
"""
import argparse
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCHEMA = ROOT / "migrations_sqlite" / "0001_init.sql"
SQLC = ROOT / "sqlc.yaml"
QUERIES = ROOT / "internal" / "store" / "queries"

# Same column-name heuristic as nullable_timestamp_scan.py.
TIMESTAMP = re.compile(r"(at|until|since|time)$")

BARE_REF = re.compile(r"^(?:DISTINCT\s+)?([\w]+\.[\w]+|[\w]+)$", re.I)
WRAPPED = re.compile(r"^coalesce\s*\(\s*[\w.]+\s*,\s*0\s*\)\s+AS\s+(\w+)$", re.I)
ALIASED = re.compile(r"\bAS\s+(\w+)\s*$", re.I)


def schema_tables():
    """Table column order plus the set of nullable timestamp column names."""
    order, nullable = {}, set()
    for m in re.finditer(r'CREATE TABLE (?:IF NOT EXISTS )?"?(\w+)"? \((.*?)\n\);',
                         SCHEMA.read_text(), re.S):
        table, cols = m.group(1), []
        for line in m.group(2).split("\n"):
            mm = re.match(r'"(\w+)"\s+(\w+)', line.strip())
            if not mm:
                continue
            col, typ = mm.group(1), mm.group(2)
            cols.append(col)
            if typ == "INTEGER" and "NOT NULL" not in line and TIMESTAMP.search(col):
                nullable.add(col)
        order[table] = cols
    return order, nullable


def query_files():
    out = []
    for line in SQLC.read_text().split("\n"):
        m = re.search(r'queries/([^"/]+)\.sql', line)
        if m:
            out.append(m.group(1))
    return out


def comment_blank(seg):
    """Copy of seg where `--` comment content is blanked (offsets preserved)."""
    out, in_line = [], False
    for ch in seg:
        if in_line:
            out.append("\n" if ch == "\n" else " ")
            continue
        if ch == "-":
            out.append("-")  # decide below on the next char
            continue
        out.append(ch)
    # second pass: blank from '--' to end of line
    text = "".join(out)
    lines = text.split("\n")
    for i, line in enumerate(lines):
        pos = line.find("--")
        if pos >= 0:
            lines[i] = line[:pos] + " " * (len(line) - pos)
    return "\n".join(lines)


def projection_spans(cf):
    """Top-level SELECT and RETURNING projection spans as (start, end).

    end is the offset of the terminating FROM (SELECT) or `;`/EOF
    (RETURNING). Spans that cannot be resolved are returned with end=None.
    """
    spans, depth, i, n = [], 0, 0, len(cf)
    while i < n:
        c = cf[i]
        if c == "(":
            depth += 1
            i += 1
            continue
        if c == ")":
            depth -= 1
            i += 1
            continue
        if depth == 0 and (c.isalpha() or c == "_"):
            m = re.match(r"[A-Za-z_]\w*", cf[i:])
            word = m.group(0)
            up = word.upper()
            if up in ("SELECT", "RETURNING"):
                spans.append({"kw": up, "start": i + len(word), "end": None})
            elif up == "FROM" and spans:
                for s in reversed(spans):
                    if s["end"] is None:
                        if s["kw"] == "SELECT":
                            s["end"] = i
                        break
            i += len(word)
            continue
        i += 1
    for s in spans:
        if s["end"] is None and s["kw"] == "RETURNING":
            semi = cf.find(";", s["start"])
            s["end"] = semi if semi >= 0 else len(cf.rstrip())
    return [s for s in spans]


def wrap_col(col, nullable):
    if col in nullable:
        return f"coalesce({col}, 0) AS {col}"
    return col


def transform_items(text, nullable, stats):
    lead = text[: len(text) - len(text.lstrip())]
    trail = text[len(text.rstrip()):]
    items = split_top(text)
    changed = []
    for item in items:
        core = item.strip()
        new, verdict = item, "keep"
        m = WRAPPED.match(core)
        if m and m.group(1) in nullable:
            verdict = "ok"
        elif BARE_REF.match(core):
            ref = BARE_REF.match(core).group(1)
            col = ref.split(".")[-1]
            if col in nullable:
                new, verdict = f"coalesce({ref}, 0) AS {col}", "wrap"
        elif ALIASED.search(core) and ALIASED.search(core).group(1) in nullable:
            verdict = "manual"
        if verdict == "wrap":
            stats["wrapped"] += 1
        elif verdict == "manual":
            stats.setdefault("manual", []).append(core)
        changed.append(new if verdict == "wrap" else item)
    # keep the same span text if nothing changed, so untouched statements
    # produce no diff at all; preserve the span's own leading/trailing
    # whitespace (the spaces that glue the list to SELECT/FROM/RETURNING)
    if all(c == i for c, i in zip(changed, items)):
        return text
    return lead + ", ".join(x.rstrip() for x in changed) + trail


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


def expand_stars(seg, cf, order, nullable, stats, where):
    """Rewrite SELECT * / RETURNING * into schema-order column lists."""
    changed = seg

    def table_of(patterns):
        for pat in patterns:
            m = re.search(pat, cf, re.I)
            if m:
                return m.group(1)
        return None

    join_warn = re.search(r"\bJOIN\b", cf, re.I) is not None

    def repl_select(m):
        t = m.group(1)
        if t not in order:
            stats.setdefault("star-unresolved", []).append(f"{where}: SELECT * FROM {t}")
            return m.group(0)
        if join_warn:
            stats.setdefault("star-unresolved", []).append(f"{where}: SELECT * with JOIN")
            return m.group(0)
        stats["star-expanded"] += 1
        return "SELECT " + ", ".join(wrap_col(c, nullable) for c in order[t]) + " FROM " + t

    changed = re.sub(r"SELECT\s+\*\s+FROM\s+([A-Za-z_]\w*)", repl_select, changed, flags=re.I)

    def repl_returning(m):
        t = table_of([r"\bINSERT INTO\s+([A-Za-z_]\w*)", r"\bUPDATE\s+([A-Za-z_]\w*)",
                      r"\bDELETE FROM\s+([A-Za-z_]\w*)"])
        if t not in (order or {}):
            stats.setdefault("star-unresolved", []).append(f"{where}: RETURNING * (table?)")
            return m.group(0)
        if join_warn:
            stats.setdefault("star-unresolved", []).append(f"{where}: RETURNING * with JOIN")
            return m.group(0)
        stats["star-expanded"] += 1
        return "RETURNING " + ", ".join(wrap_col(c, nullable) for c in order[t])

    changed = re.sub(r"RETURNING\s+\*", repl_returning, changed, flags=re.I)
    return changed


def process_file(name, order, nullable, dry):
    path = QUERIES / f"{name}.sql"
    text = path.read_text()
    stats = {"star-expanded": 0, "wrapped": 0, "manual": [], "star-unresolved": []}
    # split into statement segments at `-- name:` markers
    markers = list(re.finditer(r"^-- name: (\w+) :(\w+).*$", text, re.M))
    pieces = []
    if not markers:
        return text, stats
    pieces.append(text[: markers[0].start()])
    for idx, m in enumerate(markers):
        start = m.start()
        end = markers[idx + 1].start() if idx + 1 < len(markers) else len(text)
        seg = text[start:end]
        cf = comment_blank(seg)
        seg2 = expand_stars(seg, cf, order, nullable, stats, m.group(1))
        if seg2 != seg:
            seg, cf = seg2, comment_blank(seg2)
        # wrap bare refs inside top-level projection spans
        rebuilt, last = [], 0
        for span in projection_spans(cf):
            if span["end"] is None:
                stats.setdefault("manual", []).append(f"{m.group(1)}: unresolved {span['kw']} span")
                continue
            raw = seg[span["start"]:span["end"]]
            new = transform_items(raw, nullable, stats)
            if new != raw:
                rebuilt.append(seg[last:span["start"]])
                rebuilt.append(new)
                last = span["end"]
        if last:
            rebuilt.append(seg[last:])
            seg = "".join(rebuilt)
        pieces.append(seg)
    out = "".join(pieces)
    return out, stats


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()
    order, nullable = schema_tables()
    total = {"star-expanded": 0, "wrapped": 0}
    for name in query_files():
        out, stats = process_file(name, order, nullable, args.dry_run)
        path = QUERIES / f"{name}.sql"
        if out != path.read_text():
            if not args.dry_run:
                path.write_text(out)
            print(f"{name}: +{stats['star-expanded']} star, +{stats['wrapped']} wrapped")
        for x in stats.get("manual", []):
            print(f"  MANUAL {name}: {x}")
        for x in stats.get("star-unresolved", []):
            print(f"  UNRESOLVED {x}")
        total["star-expanded"] += stats["star-expanded"]
        total["wrapped"] += stats["wrapped"]
    print(f"total: {total['star-expanded']} `*` expansions, {total['wrapped']} bare refs wrapped"
          + (" (dry run)" if args.dry_run else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
