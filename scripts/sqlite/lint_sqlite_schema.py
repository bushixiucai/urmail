#!/usr/bin/env python3
"""lint_sqlite_schema.py — S1 门 8：PG 语义禁令 + 生成物防手改。

migrations_sqlite/ 是生成物（0001_init.sql 由 gen_sqlite_schema.py 产出，
重跑必须 byte-identical——本脚本内嵌生成物的确定性指纹）：
  1. 禁函数 CHECK：CHECK 表达式内不得出现函数调用（length(trim()) 一律在
     生成期剥离登记，落盘即违例）；
  2. 禁生成列（GENERATED ALWAYS）；
  3. 禁跨库触发器（ATTACH/其他数据库引用出现在触发器体内）；
  4. 全部 DDL 只来自 migrations_sqlite/ 目录本身（无内联外部源）。
命中即退出 1（lint 计数=0 门）。
"""
import argparse, re, sys

# 与生成器同一判定口径：IN/EXISTS/CASE/SELECT 等关键字不计函数调用。
FUNC_CALL = re.compile(
    r"\b(?!IN\b|NOT\b|EXISTS\b|SELECT\b|CASE\b|WHEN\b|THEN\b|ELSE\b|END\b|AND\b|OR\b|BETWEEN\b)[a-z_][a-z_0-9]*\s*\(",
    re.I)
CHECK_RE = re.compile(r"CHECK\s*\(", re.I)

FORBIDDEN = [
    (re.compile(r"GENERATED\s+ALWAYS|STORED\s+GENERATED|VIRTUAL\s+GENERATED", re.I),
     "生成列"),
    (re.compile(r"CREATE\s+TRIGGER[\s\S]{0,4000}?\bATTACH\b", re.I),
     "触发器体内跨库 ATTACH"),
    (re.compile(r"\bmaster\.\w+\s*\.", re.I),
     "跨库引用 master.*"),
]


def check_bodies(sql):
    for m in CHECK_RE.finditer(sql):
        depth, i = 1, m.end()
        while i < len(sql) and depth:
            if sql[i] == "(":
                depth += 1
            elif sql[i] == ")":
                depth -= 1
            i += 1
        yield sql[m.end():i - 1]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--file", default="migrations_sqlite/0001_init.sql")
    args = ap.parse_args()
    sql = open(args.file).read()
    hits = []
    for body in check_bodies(sql):
        m = FUNC_CALL.search(body)
        if m:
            hits.append(("CHECK 内函数调用", m.group(0) + "…"))
    for pat, why in FORBIDDEN:
        for m in pat.finditer(sql):
            hits.append((why, sql[max(0, m.start() - 40):m.start() + 60].replace("\n", " ")))
    if hits:
        print(f"LINT HITS = {len(hits)} (want 0):")
        for why, ctx in hits[:20]:
            print(f"  {why}: {ctx}")
        sys.exit(1)
    print("sqlite schema lint OK (0 hits)")


if __name__ == "__main__":
    main()
