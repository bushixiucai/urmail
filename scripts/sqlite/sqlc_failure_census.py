#!/usr/bin/env python3
"""sqlc_failure_census.py — B5 前失败面普查（QA 预审 ③）。

对每个 query 文件单独跑 sqlc（sqlite 引擎）归因语法失败：输出
文件→首个错误（含 sqlc 给出的行号）→该行所属方法，形成可追踪的失败面清单。
用法：python3 scripts/sqlite/sqlc_failure_census.py --sqlc <path-to-sqlc>
"""
import argparse, glob, json, os, re, subprocess, sys, tempfile

YAML = '''version: "2"
sql:
  - engine: "sqlite"
    schema: "%s"
    queries: "%s"
    gen:
      go:
        package: "store"
        out: "%s"
        sql_package: "database/sql"
'''


def method_at(lines, lineno):
    cur = None
    for i, line in enumerate(lines, 1):
        if i > lineno:
            break
        m = re.match(r'-- name:\s*(\w+)', line)
        if m:
            cur = m.group(1)
    return cur


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--sqlc", required=True)
    ap.add_argument("--out", default="scripts/sqlite/sqlc-failure-census.json")
    args = ap.parse_args()
    root = os.getcwd()
    census = []
    failed = 0
    for path in sorted(glob.glob(os.path.join(root, 'internal/store/queries/*.sql'))):
        with tempfile.TemporaryDirectory() as td:
            # sqlc resolves schema/queries RELATIVE to the config file; the
            # config therefore lives in the repo root with repo-relative
            # paths (absolute paths break: they are joined onto the config dir).
            cfg = os.path.join(root, ".sqlc-census.yaml")
            outdir = os.path.join(td, "gen")
            rel_query = os.path.relpath(path, root)
            open(cfg, "w").write(YAML % ("migrations_sqlite", rel_query, outdir))
            try:
                proc = subprocess.run([args.sqlc, "generate", "-f", cfg], capture_output=True, text=True, errors="replace")
            finally:
                os.remove(cfg)
        if proc.returncode != 0:
            failed += 1
            lines = open(path).read().split("\n")
            m = re.search(r'line (\d+):(\d+)', proc.stderr) or re.search(r':(\d+):(\d+):', proc.stderr)
            ln = int(m.group(1)) if m else 0
            err_text = " | ".join(proc.stderr.strip().split("\n")[:3])[:300]
            if "UPDATE" in err_text and "no viable alternative" in err_text:
                cat = "update-alias"
            elif "MATERIALIZED" in err_text:
                cat = "dml-cte"
            elif "does not exist" in err_text or "ambiguous" in err_text:
                cat = "cte-scope"
            else:
                cat = "syntax-residue"
            census.append({"file": os.path.basename(path), "first_error_line": ln,
                           "method": method_at(lines, ln), "category": cat,
                           "error": err_text})
    json.dump({"total_files": len(glob.glob('internal/store/queries/*.sql')),
               "failed_files": failed, "census": census},
              open(args.out, "w"), indent=1, ensure_ascii=False)
    print(json.dumps({"files": len(glob.glob('internal/store/queries/*.sql')),
                      "failed": failed}, ensure_ascii=False))


if __name__ == "__main__":
    main()
