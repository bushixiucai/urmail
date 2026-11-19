#!/usr/bin/env python3
"""rewrite_queries.py — S2：internal/store/queries 的 SQLite 方言机械改写。

覆盖（可机械判定的部分，逐类计数）：
  1. 占位符 $N → ?N（SQLite 编号参数，同一 $1 复用仍指向同一参数）
  2. FOR UPDATE / FOR SHARE（含 OF/NOWAIT/SKIP LOCKED）删除（单写者语义）
  3. ILIKE → LIKE（ASCII 不敏感；敏感场景单列清单人工复核）
  4. now()/CURRENT_TIMESTAMP/statement_timestamp() → unix 微秒表达式；
     now() + interval 'N days' / 'HH:MM:SS' → 微秒算术
  5. ::cast 清理（::type / ::type(n) / ::type[]）
  6. = ANY($N) → IN (SELECT value FROM json_each(?N))；= ANY(ARRAY[...]) → IN (...)
  7. jsonb 操作符与函数：->>'k' / ->'k' → json_extract；#>>'{p}' → json_extract；
     jsonb_build_object → json_object；jsonb_typeof → json_type；
     jsonb_array_elements → json_each；string_agg → group_concat；
     GREATEST/LEAST → max/min
  8. scope 注解：每条 `-- name:` 之上插入 `-- scope: tenant|platform`
     （platform 判定：方法名 Platform* 或仅触达 shared 表——沿用 S0 分类清单）

不可机械改写的（DISTINCT ON、LATERAL、@>、EXTRACT 非 epoch、数组函数）输出
到 manual-todo 清单，人工逐条处理，绝不静默放过。
"""
import argparse, glob, json, os, re, sys

PLATFORM_METHOD = re.compile(r"^Platform")
FOR_UPDATE = re.compile(r"\s+FOR\s+(?:UPDATE|SHARE)(?:\s+OF\s+[\w.,\s]+?)?(?:\s+(?:NOWAIT|SKIP\s+LOCKED))?(?=\s|$|;|\))", re.I)
NOW_MICRO = "(CAST(strftime('%s','now') AS INTEGER)*1000000)"
NOW_PLUS = re.compile(r"\(?\b(?:now\(\)|CURRENT_TIMESTAMP|statement_timestamp\(\))\s*\+\s*interval\s*'"
                      r"(?:(\d+)\s*days?|(\d+:\d+:\d+)|\'?\s*(?:(\d+)\s*hours?)?\s*(?:(\d+)\s*minutes?)?)''?", re.I)
INTERVAL_LIT = re.compile(r"interval\s*'(\d+)\s*(days?|hours?|minutes?|seconds?)'", re.I)
CAST = re.compile(r"::\s*(?:text|bigint|integer|int|smallint|boolean|bool|uuid|inet|jsonb|json|"
                  r"timestamptz|timestamp(?:\s+with\s+time\s+zone)?|date|numeric|decimal|double\s+precision|real|"
                  r"character\s+varying|varchar|text\[\]|bigint\[\])(\[\])?(\(\d+(?:,\d+)?\))?", re.I)
ANY_PARAM = re.compile(r"([\w.\"]+)\s*=\s*ANY\s*\(\s*\$(\d+)\s*\)", re.I)
ANY_ARRAY = re.compile(r"([\w.\"]+)\s*=\s*ANY\s*\(\s*ARRAY\s*\[([^]]*)\]\s*\)", re.I)
JSON_ARROW = re.compile(r"([\w.\"]+)\s*->>\s*'([^']*)'")
JSON_ARROW2 = re.compile(r"([\w.\"]+)\s*->\s*'([^']*)'")
JSON_HASH = re.compile(r"([\w.\"]+)\s*#>>\s*'\{([^}]*)\}'")
PARAM = re.compile(r"\$(\d+)")
MANUAL_PATTERNS = {
    "DISTINCT ON": re.compile(r"DISTINCT\s+ON", re.I),
    "LATERAL": re.compile(r"\bLATERAL\b", re.I),
    "jsonb @>": re.compile(r"@>|<@"),
    "EXTRACT(epoch)": re.compile(r"EXTRACT\s*\(\s*EPOCH", re.I),
    "EXTRACT(other)": re.compile(r"EXTRACT\s*\(", re.I),
    "array funcs": re.compile(r"\b(?:array_agg|array_position|array_length|unnest|jsonb_path)\s*\(", re.I),
    "make_interval": re.compile(r"make_interval\s*\(", re.I),
    "to_char": re.compile(r"\bto_char\s*\(", re.I),
}


def rewrite_statement(sql):
    notes = []
    s = sql
    # 1) now()+interval arithmetic first (before generic now()).
    def plus(m):
        if m.group(1):
            off = int(m.group(1)) * 86400 * 1000000
        elif m.group(2):
            h, mi, sec = (int(x) for x in m.group(2).split(":"))
            off = (h * 3600 + mi * 60 + sec) * 1000000
        else:
            off = 0
        return f"({NOW_MICRO} + {off})" if off else NOW_MICRO
    s = NOW_PLUS.sub(plus, s)
    # 2) explicit interval literals (bare) → micro seconds
    def interval(m):
        n, unit = int(m.group(1)), m.group(2).lower()
        mult = {"day": 86400, "days": 86400, "hour": 3600, "hours": 3600,
                "minute": 60, "minutes": 60, "second": 1, "seconds": 1}[unit]
        return str(n * mult * 1000000)
    s = INTERVAL_LIT.sub(interval, s)
    # 3) now()/current_timestamp/statement_timestamp bare
    s = re.sub(r"\b(now\(\)|CURRENT_TIMESTAMP|statement_timestamp\(\)|transaction_timestamp\(\)|clock_timestamp\(\))(?:\s*::\s*timestamptz)?", NOW_MICRO, s)
    # 4) jsonb arrows → json_extract
    s = JSON_ARROW.sub(lambda m: f"json_extract({m.group(1)}, '$.{m.group(2)}')", s)
    s = JSON_ARROW2.sub(lambda m: f"json_extract({m.group(1)}, '$.{m.group(2)}')", s)
    def hash_path(m):
        path = ".".join(p for p in m.group(2).split(","))
        return "json_extract(" + m.group(1) + ", '$." + path + "')"
    s = JSON_HASH.sub(hash_path, s)
    s = re.sub(r"\bjsonb_build_object\s*\(", "json_object(", s, flags=re.I)
    s = re.sub(r"\bjsonb_typeof\s*\(", "json_type(", s, flags=re.I)
    s = re.sub(r"\bjsonb_array_elements\s*\(", "json_each(", s, flags=re.I)
    s = re.sub(r"\bjsonb_array_length\s*\(", "json_array_length(", s, flags=re.I)
    s = re.sub(r"\bstring_agg\s*\(", "group_concat(", s, flags=re.I)
    s = re.sub(r"\bGREATEST\s*\(", "max(", s, flags=re.I)
    s = re.sub(r"\bLEAST\s*\(", "min(", s, flags=re.I)
    # 5) ANY forms → IN
    s = ANY_ARRAY.sub(lambda m: f"{m.group(1)} IN ({m.group(2)})", s)
    s = ANY_PARAM.sub(lambda m: f"{m.group(1)} IN (SELECT value FROM json_each(${m.group(2)}))", s)
    # 6) FOR UPDATE/SHARE removal
    s = FOR_UPDATE.sub("", s)
    # 7) casts (after intervals/jsonb so their casts are already consumed)
    s = CAST.sub("", s)
    # 8) ILIKE → LIKE
    s = re.sub(r"\bILIKE\b", "LIKE", s, flags=re.I)
    # 9) placeholders $N → ?N
    s = PARAM.sub(lambda m: "?" + m.group(1), s)
    if "ILIKE" in sql.upper():
        notes.append("ILIKE→LIKE")
    for why, pat in MANUAL_PATTERNS.items():
        if pat.search(s):
            notes.append("MANUAL: " + why)
    return s, notes


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", default="internal/store/queries")
    ap.add_argument("--write", action="store_true")
    ap.add_argument("--report", default="/tmp/p1facts/s2-rewrite-report.json")
    ap.add_argument("--platform-methods", default="scripts/sqlite/platform-methods.txt")
    args = ap.parse_args()

    platform_methods = set()
    if os.path.exists(args.platform_methods):
        platform_methods = {l.strip() for l in open(args.platform_methods) if l.strip() and not l.startswith("#")}

    before = {"FOR UPDATE": 0, "FOR SHARE": 0, "ANY": 0, "DISTINCT ON": 0, "LATERAL": 0,
              "jsonb": 0, "cast ::": 0, "ILIKE": 0, "now-family": 0, "interval": 0}
    after = dict(before)
    manual = []
    scopes = {"tenant": 0, "platform": 0}
    statements = 0
    for path in sorted(glob.glob(os.path.join(args.dir, "*.sql"))):
        src = open(path).read()
        # count before
        before["FOR UPDATE"] += len(re.findall(r"FOR\s+UPDATE", src, re.I))
        before["FOR SHARE"] += len(re.findall(r"FOR\s+SHARE", src, re.I))
        before["ANY"] += len(re.findall(r"ANY\s*\(", src, re.I))
        before["DISTINCT ON"] += len(re.findall(r"DISTINCT\s+ON", src, re.I))
        before["LATERAL"] += len(re.findall(r"\bLATERAL\b", src, re.I))
        before["jsonb"] += len(re.findall(r"jsonb", src, re.I))
        before["cast ::"] += len(re.findall(r"::", src))
        before["ILIKE"] += len(re.findall(r"\bILIKE\b", src, re.I))
        before["now-family"] += len(re.findall(r"\b(now\(\)|statement_timestamp\(\)|CURRENT_TIMESTAMP)", src, re.I))
        before["interval"] += len(re.findall(r"interval\s*'", src, re.I))

        parts = re.split(r"(?m)^(-- name:\s*\w+.*)$", src)
        out = [parts[0]]
        for i in range(1, len(parts), 2):
            header, body = parts[i], parts[i + 1] if i + 1 < len(parts) else ""
            m = re.match(r"-- name:\s*(\w+)", header)
            method = m.group(1) if m else ""
            scope = "platform" if (method in platform_methods or PLATFORM_METHOD.match(method)) else "tenant"
            scopes[scope] += 1
            statements += 1
            rewritten, notes = rewrite_statement(body)
            if notes:
                manual.append({"method": method, "notes": notes, "file": os.path.basename(path)})
            out.append(header + "\n-- scope: " + scope + "\n" + rewritten)
        new = "".join(out)
        after["FOR UPDATE"] += len(re.findall(r"FOR\s+UPDATE", new, re.I))
        after["FOR SHARE"] += len(re.findall(r"FOR\s+SHARE", new, re.I))
        after["ANY"] += len(re.findall(r"ANY\s*\(", new, re.I))
        after["DISTINCT ON"] += len(re.findall(r"DISTINCT\s+ON", new, re.I))
        after["LATERAL"] += len(re.findall(r"\bLATERAL\b", new, re.I))
        after["jsonb"] += len(re.findall(r"jsonb", new, re.I))
        after["cast ::"] += len(re.findall(r"::", new))
        after["ILIKE"] += len(re.findall(r"\bILIKE\b", new, re.I))
        after["now-family"] += len(re.findall(r"\b(now\(\)|statement_timestamp\(\)|CURRENT_TIMESTAMP)", new, re.I))
        after["interval"] += len(re.findall(r"interval\s*'", new, re.I))
        if args.write:
            open(path, "w").write(new)

    report = {"statements": statements, "scopes": scopes, "before": before, "after": after,
              "manual_todo": manual}
    json.dump(report, open(args.report, "w"), indent=1, ensure_ascii=False)
    print(json.dumps({"statements": statements, "scopes": scopes,
                      "after": after, "manual": len(manual)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
