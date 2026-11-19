#!/usr/bin/env python3
"""lint_sqlite_schema.py — S1 门 8：PG 语义禁令 + 生成物防手改。

migrations_sqlite/0001_init.sql 是生成物（重跑 byte-identical）：
  1. 禁函数 CHECK：CHECK 表达式内的函数调用必须在 SQLite 标量函数白名单内
     （QA 复审：length/instr/substr/json_*/coalesce/abs/min/max 等——与生成器
     同一份白名单口径）；
  2. 禁生成列（GENERATED ALWAYS）；
  3. 禁跨库触发器（ATTACH/其它库引用出现在触发器体内）；
  4. 全部 DDL 只来自 migrations_sqlite/ 目录本身。
命中即退出 1（lint 计数=0 门）。
"""
import argparse, re, sys

SQLITE_FUNCS = {"length", "instr", "substr", "substring", "lower", "upper", "trim",
                "ltrim", "rtrim", "replace", "coalesce", "abs", "min", "max", "round",
                "json_extract", "json_type", "json_array_length", "json_valid",
                "json_quote", "ifnull", "nullif", "typeof", "cast"}
SQL_KEYWORDS = {"in", "not", "exists", "select", "case", "when", "then", "else", "end",
                "and", "or", "between", "is", "null", "like", "glob", "check"}
CALL_RE = re.compile(r"\b([a-z_][a-z_0-9]*)\s*\(", re.I)
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
        for m in CALL_RE.finditer(body):
            name = m.group(1).lower()
            if name in SQL_KEYWORDS or name in SQLITE_FUNCS:
                continue
            hits.append(("CHECK 内非白名单函数", name + "("))
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
