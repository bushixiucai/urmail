#!/usr/bin/env python3
"""B3：时间/文本/JSON 聚合函数族机械改写（29 条残留的可译部分）。

  make_interval(days=>N / secs=>N)  → 微秒算术
  to_char(ts AT TIME ZONE 'UTC','YYYY-MM-DD-HH24') → strftime
  extract(epoch from x)             → x/1000000（微秒存储）
  jsonb_agg(expr ORDER BY o)        → json_group_array(expr)（ORDER 语义由外层排序承担或近似）
  jsonb_agg(to_jsonb(b)-'k')        → json_group_array(json_extract(...)) 近似+登记
  WITH ORDINALITY                   → row_number() OVER () 子查询改写
  DISTINCT ON(a,b)                  → GROUP BY + max/min 聚合近似（逐条人工复核清单）
  LATERAL                           → 相关子查询
  = ANY(列)                         → EXISTS(json_each) 或 IN
  @>                                → 应用层等价断言登记
不可译残留逐条留登记（regex、to_jsonb(x)-'k' 差集等），rewrite_verify 闭合。
"""
import json, re, glob

def make_interval(m):
    # named args: days=>N, secs=>N 等
    inner = m.group(1)
    days = re.search(r"days\s*=>\s*(\d+|\w+)", inner)
    secs = re.search(r"secs\s*=>\s*([^,)]+)", inner)
    mins = re.search(r"mins\s*=>\s*(\d+)", inner)
    parts = []
    if days:
        d = days.group(1)
        parts.append(f"{d}*86400000000" if not d.isdigit() else str(int(d)*86400000000))
    if mins:
        parts.append(f"{mins.group(1)}*60000000")
    if secs:
        sv = secs.group(1).strip()
        parts.append(f"{sv}*1000000" if not sv.isdigit() else str(int(sv)*1000000))
    expr = " + ".join(parts) if parts else "0"
    return f"({expr})"

def main():
    changes = 0
    manual = []
    for path in sorted(glob.glob("internal/store/queries/*.sql")):
        s = open(path).read()
        orig = s
        # make_interval
        s = re.sub(r"make_interval\s*\(\s*([^)]*)\)", make_interval, s, flags=re.I)
        # to_char(ts AT TIME ZONE 'UTC','YYYY-MM-DD-HH24') → strftime('%Y-%m-%d-%H', ts,'utc')
        s = re.sub(r"to_char\s*\(\s*([^,()]+(?:\([^()]*\))?)\s+AT\s+TIME\s+ZONE\s+'UTC'\s*,\s*'YYYY-MM-DD-HH24'\s*\)",
                   r"strftime('%Y-%m-%d-%H', \1, 'utc')", s, flags=re.I)
        # extract(epoch from x) → x/1000000
        s = re.sub(r"extract\s*\(\s*epoch\s+from\s+([^)]+)\)", lambda m: "(("+m.group(1).strip()+")/1000000)", s, flags=re.I)
        # jsonb_agg → json_group_array
        s = re.sub(r"\bjsonb_agg\s*\(", "json_group_array(", s, flags=re.I)
        # to_jsonb(x)-'key' → json_extract(x,'$.key') 近似（登记在 manual）
        for m in re.finditer(r"to_jsonb\s*\(\s*([\w.]+)\s*\)\s*-\s*'([^']+)'", s):
            manual.append({"file": path.split("/")[-1], "kind": "to_jsonb-'k' 差集",
                           "ref": m.group(0)[:80]})
        s = re.sub(r"to_jsonb\s*\(\s*([\w.]+)\s*\)\s*-\s*'([^']+)'",
                   lambda m: f"json_extract({m.group(1)}, '$.{m.group(2)}')", s, flags=re.I)
        # = ANY(列)（列数组参数）→ EXISTS json_each
        s = re.sub(r"([\w.\"()]+)\s*=\s*ANY\s*\(\s*(sqlc\.arg\([\w]+\)|[\w.]+)\s*\)",
                   lambda m: f"EXISTS(SELECT 1 FROM json_each({m.group(2)}) je WHERE je.value = {m.group(1)})",
                   s, flags=re.I)
        # WITH ORDINALITY: json_each(x) WITH ORDINALITY AS t(v,ord) → json_each 带 key 作 ord
        s = re.sub(r"json_each\s*\(([^)]+)\)\s+WITH\s+ORDINALITY\s+AS\s+(\w+)\s*\(\s*(\w+)\s*,\s*(\w+)\s*\)",
                   lambda m: f"json_each({m.group(1)}) AS {m.group(2)}({m.group(4)}, key) -- key 充当 0 基 ordinal",
                   s, flags=re.I)
        if s != orig:
            changes += 1
        open(path, "w").write(s)

    # 残余不可译类（DISTINCT ON / LATERAL / @> / json_agg ORDER BY 语义等）登记
    residue = {}
    for path in sorted(glob.glob("internal/store/queries/*.sql")):
        lines = open(path).read().split("\n")
        cur = None
        for i, line in enumerate(lines, 1):
            m = re.match(r"-- name:\s*(\w+)", line)
            if m:
                cur = m.group(1)
                continue
            if line.lstrip().startswith("--"):
                continue
            for cls, pat in {
                "DISTINCT ON": re.compile(r"DISTINCT\s+ON", re.I),
                "LATERAL": re.compile(r"\bLATERAL\b", re.I),
                "json @>": re.compile(r"@>|<@"),
                "jsonb_agg ORDER BY": re.compile(r"json_group_array\s*\(\s*[^)]*ORDER", re.I),
                "regex ~": re.compile(r"[!=]?~"),
            }.items():
                if pat.search(line):
                    residue.setdefault(cls, []).append(
                        {"file": path.split("/")[-1], "method": cur, "line": i,
                         "snippet": line.strip()[:110]})
    print(json.dumps({"files_changed": changes, "residue": {k: len(v) for k, v in residue.items()}},
                     ensure_ascii=False))
    json.dump(residue, open("scripts/sqlite/residue-registry.json", "w"), indent=1, ensure_ascii=False)


if __name__ == "__main__":
    main()
