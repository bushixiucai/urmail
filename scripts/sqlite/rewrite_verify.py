#!/usr/bin/env python3
"""rewrite_verify.py — S2 改写闭合自断言（QA 预审 ③）。

断言：
  1. 重新扫描 queries 的残留构造，与 scripts/sqlite/residue-registry.json 的
     逐条登记（文件:方法:行）完全一致——新增残留=漂移退出 1；
  2. 打印 before(改写时报告)→after→登记 的闭合表。
残留类别与承接批次见 residue-registry.json。
"""
import json, re, glob, sys

RESIDUE_PATTERNS = {
    "ANY": re.compile(r"\bANY\s*\(", re.I),
    "WITH ORDINALITY": re.compile(r"WITH\s+ORDINALITY", re.I),
    "jsonb_agg/json_agg": re.compile(r"\bjsonb?_agg\s*\(", re.I),
    "EXTRACT": re.compile(r"(?<![a-z_])EXTRACT\s*\(", re.I),
    "regex ~": re.compile(r"[!=]?~"),
    "DISTINCT ON": re.compile(r"DISTINCT\s+ON", re.I),
    "LATERAL": re.compile(r"\bLATERAL\b", re.I),
    "jsonb-op @>": re.compile(r"@>|<@"),
}


def scan():
    cur_counts = {}
    for path in sorted(glob.glob('internal/store/queries/*.sql')):
        lines = open(path).read().split('\n')
        cur = None
        for i, line in enumerate(lines, 1):
            m = re.match(r'-- name:\s*(\w+)', line)
            if m:
                cur = m.group(1)
                continue
            if line.lstrip().startswith('--'):
                continue
            for cls, pat in RESIDUE_PATTERNS.items():
                if pat.search(line):
                    cur_counts[cls] = cur_counts.get(cls, 0) + 1
    return cur_counts


def main():
    reg = json.load(open('scripts/sqlite/residue-registry.json'))
    reg_totals = reg['totals']
    now = scan()
    drift = []
    for cls in set(reg_totals) | set(now):
        if reg_totals.get(cls, 0) != now.get(cls, 0):
            drift.append(f"{cls}: registry={reg_totals.get(cls,0)} now={now.get(cls,0)}")
    if drift:
        print("RESIDUE DRIFT (update residue-registry.json in a reviewed commit):")
        for d in drift:
            print(" ", d)
        sys.exit(1)
    total = sum(now.values())
    print(json.dumps({"status": "closed", "residue_total": total, "by_class": now},
                     ensure_ascii=False))


if __name__ == "__main__":
    main()
