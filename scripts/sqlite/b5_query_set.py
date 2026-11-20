#!/usr/bin/env python3
"""b5_query_set.py — keep the SQLite sqlc query set provably equal to the files
that are allowed to generate, and keep the platform blocker ledger complete.

B5a switches sqlc to the SQLite engine. Files whose statements are all
`-- scope: platform` are *management-plane* SQL that the SQLite port cannot
serve yet; their routes must answer 501 instead of failing as a silent 500.
Those files therefore must not enter the sqlc query list, and every one of
them must have an entry in scripts/sqlite/b5-blocked-files.json.

Checks (default `--check`):
  * the blocked ledger lists exactly the platform-only query files;
  * each ledger entry carries file / first_error / category / reason /
    s4_owner / route_status=501;
  * sqlc.yaml's explicit `queries:` list is exactly the complementary set
    (tenant + notes-only files), in sorted order, with no duplicates.

`--write-blocked --census <json>` rebuilds the ledger from a fresh
sqlc_failure_census.py run. `--write-queries` rewrites the `queries:` block in
sqlc.yaml so the list cannot silently drift.
"""
import argparse
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
QUERY_DIR = ROOT / "internal" / "store" / "queries"
LEDGER = ROOT / "scripts" / "sqlite" / "b5-blocked-files.json"
SQLC = ROOT / "sqlc.yaml"

LEDGER_FIELDS = ("file", "first_error", "category", "reason", "s4_owner", "route_status")

REASONS = {
    "syntax-residue": "PG 语法（DML-CTE/FILTER/UPDATE…FROM/RETURNING 星号等）无法被 sqlc 的 SQLite 解析器承接，需按 S4 手写层重写",
    "ambiguous-column": "改写后列引用歧义（裸列绑到内层表），需按 origin/main 重建相关性",
    "missing-alias": "子查询别名丢失/表函数未命名，需修语句头",
    "no-queries": "文件只剩注记、无可生成语句",
    "ast-nil-name": "sqlc 解析 AST 失败（表达式形状不受支持）",
    "parsed-ok": "语句可解析，但整文件为 platform 管理面；B5a 不生成、路由暂不可用",
}

S4_OWNER = "S4/B5 手写层（platform 管理面承接；本轮只登记，不改写）"


def classify(path):
    src = path.read_text()
    scopes = set(re.findall(r"(?m)^-- scope:\s*(\w+)", src))
    return "platform" if scopes == {"platform"} else "tenant"


def split_files():
    plat, rest = [], []
    for p in sorted(QUERY_DIR.glob("*.sql")):
        (plat if classify(p) == "platform" else rest).append(p)
    return plat, rest


def statements(path):
    return re.findall(r"(?m)^-- name:\s*(\w+)", path.read_text())


def sqlc_query_list():
    src = SQLC.read_text()
    m = re.search(r"(?m)^    queries:\n((?:      - .*\n)+)", src)
    if not m:
        m = re.search(r'(?m)^    queries: "([^"]*)"\n', src)
        if not m:
            raise SystemExit("sqlc.yaml: cannot find queries block")
        return [m.group(1)]
    return re.findall(r'      - "([^"]+)"', m.group(1))


def render_queries(files):
    return "    queries:\n" + "\n".join(f'      - "{f}"' for f in files) + "\n"


def write_queries(files):
    src = SQLC.read_text()
    block = render_queries(files)
    src = re.sub(r"(?m)^    queries:\n((?:      - .*\n)+)", block.replace("\\", "\\\\"), src)
    if '    queries: "' in src:
        src = re.sub(r'(?m)^    queries: "[^"]*"\n', block.replace("\\", "\\\\"), src)
    SQLC.write_text(src)


def write_blocked(plat, census):
    by_file = {e["file"]: e for e in census}
    entries = []
    for p in plat:
        rel = str(p.relative_to(ROOT))
        c = by_file.get(p.name, {})
        err = c.get("error")
        cat = c.get("category") or ("parsed-ok" if not err else "syntax-residue")
        first = None
        if err:
            # census errors start with "# package store"; the diagnostic line
            # that actually names file:line:column comes after it.
            parts = [x.strip() for x in err.split(" | ")]
            first = next((x for x in parts if x and not x.startswith("#")), parts[0])
        entries.append({
            "file": rel,
            "statements": statements(p),
            "first_error": first,
            "first_error_line": c.get("first_error_line") or 0,
            "first_error_method": c.get("method"),
            "category": cat,
            "reason": REASONS.get(cat, REASONS["syntax-residue"]),
            "s4_owner": S4_OWNER,
            "route_status": 501,
            "route_message": "路由暂不可用（HTTP 501）：该文件属 platform 管理面，SQLite 端口尚未承接",
        })
    LEDGER.write_text(json.dumps({
        "note": "B5a blocked platform-only query files. Excluded from sqlc.yaml's query set; their routes must answer 501, never a silent 500.",
        "count": len(entries),
        "files": entries,
    }, ensure_ascii=False, indent=1) + "\n")


def check():
    plat, rest = split_files()
    errors = []
    ledger = json.loads(LEDGER.read_text())
    files = ledger.get("files", [])
    listed = {e.get("file") for e in files}
    want = {str(p.relative_to(ROOT)) for p in plat}
    if listed != want:
        errors.append(f"blocked ledger mismatch: extra={sorted(listed - want)} missing={sorted(want - listed)}")
    if ledger.get("count") != len(files):
        errors.append(f"blocked ledger count {ledger.get('count')} != {len(files)} entries")
    for e in files:
        for field in LEDGER_FIELDS:
            if field not in e:
                errors.append(f"{e.get('file')}: missing field {field}")
        if e.get("route_status") != 501:
            errors.append(f"{e.get('file')}: route_status must be 501")
        if not e.get("s4_owner"):
            errors.append(f"{e.get('file')}: missing s4_owner")
        if e.get("first_error") is None and e.get("category") != "parsed-ok" and "no queries" not in str(e.get("reason", "")):
            errors.append(f"{e.get('file')}: first_error missing for category {e.get('category')}")
    got = sqlc_query_list()
    want_q = [str(p.relative_to(ROOT)) for p in rest]
    if got != want_q:
        errors.append(
            "sqlc.yaml queries mismatch:\n  extra=" + repr(sorted(set(got) - set(want_q)))
            + "\n  missing=" + repr(sorted(set(want_q) - set(got)))
            + ("\n  order differs" if set(got) == set(want_q) else ""))
    if len(got) != len(set(got)):
        errors.append("sqlc.yaml queries has duplicates")
    if errors:
        for e in errors:
            print(e, file=sys.stderr)
        return 1
    print(json.dumps({"platform_files": len(plat), "sqlc_queries": len(got),
                      "ledger": len(files), "status": "ok"}, ensure_ascii=False))
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true")
    ap.add_argument("--write-blocked", action="store_true")
    ap.add_argument("--census")
    ap.add_argument("--write-queries", action="store_true")
    args = ap.parse_args()
    plat, rest = split_files()
    if args.write_queries:
        write_queries([str(p.relative_to(ROOT)) for p in rest])
        print(f"wrote {len(rest)} query paths to sqlc.yaml")
    if args.write_blocked:
        if not args.census:
            raise SystemExit("--write-blocked needs --census <sqlc_failure_census.json>")
        write_blocked(plat, json.load(open(args.census))["census"])
        print(f"wrote {len(plat)} blocked entries")
    if args.write_blocked or args.write_queries:
        return 0
    return check()


if __name__ == "__main__":
    raise SystemExit(main())
