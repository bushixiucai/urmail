#!/usr/bin/env python3
"""gen_sqlite_schema.py — Phase 1 S1: migrations_sqlite/0001_init.sql 生成器。

事实源：internal/testdb TestDumpCatalog 的 TSV（COLUMN/CONSTRAINT/INDEXDEF/
TRIGGER/POLICY/…段）。生成物**禁止手改**（lint 校验来源一致性）；重跑必须
byte-identical（「空库连跑两次结果一致」的生成侧对应物）。

类型映射（v3.2 §3.1，逐列入 type-map 清单）：
  timestamptz/timestamp→INTEGER(unix micro)、bigint/integer/smallint→INTEGER、
  bigserial/identity→INTEGER PRIMARY KEY AUTOINCREMENT、boolean→INTEGER、
  uuid/inet/citext/text→TEXT、jsonb/json→TEXT(JSON1)、text[]/bigint[]→TEXT
  (JSON 数组)、date→TEXT(ISO)、bytea→BLOB、numeric/decimal→TEXT(定点)。

PG 语义剥离（登记，不静默丢失）：
  - CHECK 调函数/正则操作符 → 剥离入 stripped-constraints（S4 应用层承接）；
  - 默认值含函数（now()/nextval/表达式）→ 剥离默认并登记；
  - GIN/trigram 索引 → dropped-indexes 登记（§3.5 trigram 降级）。

user_version 链：文件头注释声明版本 1，文件尾 `PRAGMA user_version = 1`；
runner（internal/sqlitedb.Migrate）只前进、gap 拒启、重跑幂等
（IF NOT EXISTS 语义）。
"""
import argparse, glob, json, os, re, sys

TYPE_MAP = [
    ("timestamp with time zone", "INTEGER"), ("timestamp without time zone", "INTEGER"),
    ("bigint", "INTEGER"), ("integer", "INTEGER"), ("smallint", "INTEGER"),
    ("boolean", "INTEGER"), ("uuid", "TEXT"), ("inet", "TEXT"), ("citext", "TEXT"),
    ("jsonb", "TEXT"), ("json", "TEXT"), ("text[]", "TEXT"), ("bigint[]", "TEXT"),
    ("integer[]", "TEXT"), ("date", "TEXT"), ("bytea", "BLOB"),
    ("numeric", "TEXT"), ("character varying", "TEXT"), ("text", "TEXT"), ("bpchar", "TEXT"),
    ("double precision", "REAL"),
]
PINNED_TYPE_COUNTS = {  # S1 验收门：catalog 实测（QA 给定）
    "timestamp with time zone": 217, "jsonb": 43, "boolean": 41, "bigint": 360,
    "uuid": 6, "inet": 2, "bytea": 5,
}
AUTOINCREMENT_LIMIT = 9007199254740991  # 与 PG 侧 ai/billing 列 CHECK 上限一致


def map_type(pg_type):
    for prefix, sqlite in TYPE_MAP:
        if pg_type == prefix or pg_type.startswith(prefix + "("):
            return sqlite, prefix
    return None, None


def load(path):
    cols, constr, indexes = [], [], []
    sec = None
    for line in open(path):
        c = line.rstrip("\n").split("\t")
        if c[0] in ("TABLE", "POLICY", "TRIGGER", "FUNCTION", "EXTENSION", "GRANT",
                    "COLUMN", "CONSTRAINT", "INDEXDEF"):
            headers = {"COLUMN": "table", "CONSTRAINT": "table", "INDEXDEF": "table"}
            if c[0] in headers and c[1] == headers[c[0]]:
                continue
            sec = c[0]
            c = c[1:]
        if sec == "COLUMN":
            if len(c) < 6:
                continue  # multiline value continuation
            cols.append({"table": c[0], "column": c[1], "type": c[2],
                         "notnull": c[3] == "true", "default": c[4], "identity": c[5]})
        elif sec == "CONSTRAINT":
            if len(c) < 4:
                continue  # multiline constraint definition continuation
            # pg "char" 扫描出 ASCII 码（c=99, f=102, p=112, u=117）——归一化
            ctype = c[2]
            ctype = {"99": "c", "102": "f", "112": "p", "117": "u"}.get(ctype, ctype)
            constr.append({"table": c[0], "name": c[1], "type": ctype, "def": "\t".join(c[3:])})
        elif sec == "INDEXDEF":
            if len(c) < 4:
                continue
            indexes.append({"table": c[0], "name": c[1], "unique": c[2] == "true", "def": "\t".join(c[3:])})
    return cols, constr, indexes


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--catalog", required=True)
    ap.add_argument("--trigger-port", default="scripts/sqlite/trigger-port.json")
    ap.add_argument("--new-tables", default="scripts/sqlite/new-tables.sql")
    ap.add_argument("--out", default="migrations_sqlite/0001_init.sql")
    ap.add_argument("--registry-out", default="migrations_sqlite/generated-artifacts.json")
    ap.add_argument("--type-map-out", default="migrations_sqlite/type-map.tsv")
    args = ap.parse_args()

    cols, constr, indexes = load(args.catalog)
    tables = sorted({c["table"] for c in cols} - {"schema_migrations"})

    by_table_cols = {}
    for c in cols:
        if c["table"] == "schema_migrations":
            continue
        by_table_cols.setdefault(c["table"], []).append(c)

    pks, uniques, fks, checks = {}, {}, {}, {}
    for k in constr:
        if k["type"] == "p":
            pks.setdefault(k["table"], []).append(k)
        elif k["type"] == "u":
            uniques.setdefault(k["table"], []).append(k)
        elif k["type"] == "f":
            fks.setdefault(k["table"], []).append(k)
        elif k["type"] == "c":
            checks.setdefault(k["table"], []).append(k)

    type_counts = {}
    type_rows = []
    unmapped = []
    stripped_defaults = []
    for c in cols:
        # 类型对账口径与 QA 实测一致：全量列（含 schema_migrations 的
        # version/dirty）；DDL 生成仍排除 schema_migrations（其 SQLite 形态
        # 由 new-tables.sql 定义，见 106 表门）。
        sqlite, prefix = map_type(c["type"])
        if sqlite is None:
            unmapped.append(f"{c['table']}.{c['column']}:{c['type']}")
            continue
        type_counts[prefix] = type_counts.get(prefix, 0) + 1
        type_rows.append((c["table"], c["column"], c["type"], sqlite))
    for k, v in PINNED_TYPE_COUNTS.items():
        if type_counts.get(k, 0) != v:
            print(f"TYPE COUNT DRIFT: {k} = {type_counts.get(k, 0)}, pinned {v}")
            sys.exit(1)
    if unmapped:
        print("UNMAPPED TYPES:")
        for u in unmapped:
            print(" ", u)
        sys.exit(1)

    # ---- PG 语义规范化 ----
    # 'x'::text → 'x'；'42'::bigint → 42；x = ANY (ARRAY[a,b,...]) → x IN (a,b,...)。
    # 规范化后仍含函数调用/PG 操作符的表达式 → 降级登记（不静默丢弃）。
    cast_re = re.compile(r"'((?:[^']|'')*)'::[a-z_ ]+")
    num_cast_re = re.compile(r"'(-?\d+)'::(?:bigint|integer|smallint|numeric|double precision)", re.I)
    any_re = re.compile(r"([\"A-Za-z_0-9.]+)\s*=\s*ANY\s*\(\s*ARRAY\s*\[([^]]*)\]\s*\)", re.I)
    func_call = re.compile(r"\b(?!IN\b|NOT\b|EXISTS\b|SELECT\b|CASE\b|WHEN\b|THEN\b|ELSE\b|END\b|AND\b|OR\b|BETWEEN\b)[a-z_][a-z_0-9]*\s*\(", re.I)
    pg_only = re.compile(r"~~|::~|!~|\s~+\s|::[a-z_]|jsonb_|to_tsvector|statement_timestamp|btrim\(|make_interval|interval '", re.I)

    def normalize(expr):
        e = num_cast_re.sub(lambda m: m.group(1), expr)
        e = cast_re.sub(lambda m: "'" + m.group(1) + "'", e)
        while True:
            e2 = any_re.sub(lambda m: f"{m.group(1)} IN ({m.group(2)})", e)
            if e2 == e:
                break
            e = e2
        return e

    def pg_untranslatable(expr):
        probe = re.sub(r"^(CHECK|CREATE|WHERE)\s*\(", "", expr.strip(), count=1)
        probe = re.sub(r"\bIN\s*\(", "", probe)
        return bool(pg_only.search(probe) or func_call.search(probe))

    # CHECK：规范化后仍不可译 → 剥离登记
    stripped_checks = []
    normalized_checks = {}
    for t, ks in checks.items():
        for k in ks:
            body = normalize(k["def"])
            if pg_untranslatable(body):
                stripped_checks.append({"table": t, "name": k["name"], "def": k["def"],
                                         "reason": "CHECK 内调函数/PG 语义不可译——S4 应用层承接"})
            else:
                normalized_checks.setdefault(t, []).append(
                    "CHECK " + body[body.index("("):].rstrip(","))

    # 默认值：去 cast 后为纯字面量则保留，否则登记
    simple_default = re.compile(r"^'([^']|'')*'$|^-?[0-9]+$")
    stripped_defaults = []
    for t, cs in by_table_cols.items():
        for c in cs:
            d = c["default"].strip()
            if not d:
                continue
            if d.startswith("nextval(") or c["identity"]:
                continue  # AUTOINCREMENT 由 PK 处理
            norm = normalize(d)
            if not simple_default.match(norm):
                stripped_defaults.append({"table": t, "column": c["column"], "default": d,
                                           "reason": "默认值含函数/表达式——SQLite 侧由应用层供给"})

    # 索引：GIN/trigram 降级登记；谓词规范化后不可译降级；其余翻译
    dropped_indexes = []
    kept_indexes = []
    ix_re = re.compile(r"CREATE (UNIQUE )?INDEX (\S+) ON (?:public\.)?(\S+)(?: USING \w+)? \((.*)\)(?: WHERE (.*))?$")
    for ix in indexes:
        if "USING gin" in ix["def"] or "gin_trgm_ops" in ix["def"]:
            dropped_indexes.append({"table": ix["table"], "name": ix["name"], "def": ix["def"],
                                    "reason": "trigram GIN 无 SQLite 对应，§3.5 降级（LIKE 或 FTS5）"})
            continue
        m = ix_re.match(ix["def"].strip())
        if not m:
            dropped_indexes.append({"table": ix["table"], "name": ix["name"], "def": ix["def"],
                                    "reason": "indexdef 无法解析——登记待人工核对"})
            continue
        uniq, name, table, ixcols, where = m.groups()
        ixcols_n, where_n = normalize(ixcols), normalize(where or "")
        if where and pg_untranslatable(where_n):
            dropped_indexes.append({"table": ix["table"], "name": ix["name"], "def": ix["def"],
                                    "reason": "部分索引 WHERE 谓词含 PG 语义——登记，S4 定夺"})
            continue
        kept_indexes.append({"table": table, "name": name, "unique": bool(uniq),
                             "cols": ixcols_n, "where": where_n})

    # ---- 生成 DDL ----
    out = []
    out.append("-- GENERATED by scripts/sqlite/gen_sqlite_schema.py — DO NOT EDIT.")
    out.append("-- Regenerate: dump catalog (internal/testdb TestDumpCatalog) then rerun;")
    out.append("-- rerun must be byte-identical. Source of truth: PG16 catalog at HEAD.")
    out.append("-- user_version chain: this file is version 1 (single init). Gap/fork refuse.")
    out.append("PRAGMA foreign_keys=ON;")
    out.append("")

    def quote(s):
        return '"' + s.replace('"', '""') + '"'

    for t in tables:
        cs = by_table_cols[t]
        pk = pks.get(t, [])
        pk_cols = []
        for k in pk:
            m = re.search(r"PRIMARY KEY\s*\(([^)]*)\)", k["def"])
            if m:
                pk_cols = [x.strip().strip('"') for x in m.group(1).split(",")]
        single_serial_pk = None
        if len(pk_cols) == 1:
            col = pk_cols[0]
            for c in cs:
                if c["column"] == col and (c["identity"] or c["default"].startswith("nextval(")):
                    single_serial_pk = col

        lines = []
        for c in cs:
            sqlite, _ = map_type(c["type"])
            if c["column"] == single_serial_pk:
                lines.append(f"  {quote(c['column'])} INTEGER PRIMARY KEY AUTOINCREMENT")
                continue
            piece = f"  {quote(c['column'])} {sqlite}"
            if c["notnull"]:
                piece += " NOT NULL"
            d = normalize(c["default"].strip())
            if d and not c["identity"] and not c["default"].strip().startswith("nextval(") and simple_default.match(d):
                piece += f" DEFAULT {d}"
            lines.append(piece)
        if pk_cols and not single_serial_pk:
            lines.append("  PRIMARY KEY (" + ", ".join(quote(x) for x in pk_cols) + ")")
        for k in uniques.get(t, []):
            m = re.search(r"UNIQUE\s*\(([^)]*)\)", k["def"])
            if m:
                lines.append("  UNIQUE (" + m.group(1) + ")")
        for k in fks.get(t, []):
            lines.append("  " + normalize(k["def"]).rstrip(","))
        for ck in normalized_checks.get(t, []):
            lines.append("  " + ck)
        out.append(f"CREATE TABLE IF NOT EXISTS {quote(t)} (")
        out.append(",\n".join(lines))
        out.append(");")
        out.append("")

    # 新表（hand-authored，生成器合并）
    out.append(open(args.new_tables).read().rstrip())
    out.append("")

    # 索引（规范化后翻译）
    for ix in kept_indexes:
        uniq = "UNIQUE " if ix["unique"] else ""
        where = f" WHERE {ix['where']}" if ix["where"] else ""
        out.append(f"CREATE {uniq}INDEX IF NOT EXISTS {ix['name']} ON {ix['table']} ({ix['cols']}){where};")
    out.append("")

    # 触发器移植层（hand-translated, auditable）
    # SQLite 触发器为单事件（无 INSERT OR UPDATE）——生成器按 OR 拆分。
    port = json.load(open(args.trigger_port))
    for fn in sorted(k for k in port if not k.startswith("_")):
        entry = port[fn]
        out.append(f"-- Port of PG function {fn} ({entry.get('note', '')})")
        for trig in entry["triggers"]:
            events = [e.strip() for e in trig["event"].split(" OR ")]
            for idx, ev in enumerate(events):
                name = trig["name"] if len(events) == 1 else f"{trig['name']}__{ev.split()[0].lower()}"
                body = trig["body"].rstrip().rstrip(";")
                out.append(f"CREATE TRIGGER IF NOT EXISTS {name}")
                out.append(f"{trig['timing']} {ev} ON {trig['table']}")
                if trig.get("when"):
                    out.append(f"WHEN {trig['when']}")
                out.append("BEGIN")
                for stmt in [s for s in body.split("\n") if s.strip()]:
                    out.append("  " + stmt.strip().rstrip(";") + ";")
                out.append("END;")
                out.append("")
        if entry.get("app_pending"):
            out.append(f"-- APP-PENDING ({fn}): {entry['app_pending']}")
            out.append("")

    # append-only 触发器（v3.2 §2.4：5 表，4 张含 no_insert）
    APPEND_ONLY = ["audit_logs", "announcements", "channels", "ai_usage_requests", "delivery_events"]
    NO_INSERT = ["audit_logs", "announcements", "channels", "ai_usage_requests"]
    for t in APPEND_ONLY:
        ops = []
        if t in NO_INSERT:
            ops.append("INSERT")
        ops += ["UPDATE", "DELETE"]
        for op in ops:
            out.append(f"CREATE TRIGGER IF NOT EXISTS {t}_append_only_{op.lower()}")
            out.append(f"BEFORE {op} ON {t}")
            out.append(f"BEGIN SELECT RAISE(ABORT, '{t} is append-only'); END;")
        out.append("")
    out.append("PRAGMA user_version = 1;")
    out.append("")

    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    open(args.out, "w").write("\n".join(out))

    json.dump({"type_counts": type_counts,
               "stripped_checks": stripped_checks,
               "stripped_defaults": stripped_defaults,
               "dropped_indexes": dropped_indexes},
              open(args.registry_out, "w"), indent=1, ensure_ascii=False)
    with open(args.type_map_out, "w") as f:
        f.write("table\tcolumn\tpg_type\tsqlite_type\n")
        for row in type_rows:
            f.write("\t".join(row) + "\n")
    print(json.dumps({"tables": len(tables) + 3, "type_counts": type_counts,
                      "stripped_checks": len(stripped_checks),
                      "stripped_defaults": len(stripped_defaults),
                      "dropped_indexes": len(dropped_indexes),
                      "kept_indexes": len(kept_indexes)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
