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
    # 'x'::text → 'x'；'42'::bigint → 42；x = ANY (ARRAY[a,b,...]) → x IN (a,b,...)；
    # btrim(x)→trim(x)；POSITION(a IN b)→instr(b,a)。
    # SQLite 可用的标量函数白名单内的 CHECK 保留；其余（正则/sha256/非确定性
    # 等）剥离登记，绝不静默丢弃。
    cast_re = re.compile(r"'((?:[^']|'')*)'::[a-z_ ]+")
    num_cast_re = re.compile(r"'(-?\d+)'::(?:bigint|integer|smallint|numeric|double precision)", re.I)
    any_re = re.compile(r"([\"A-Za-z_0-9.]+(?:\([^()]*\))?)\s*=\s*ANY\s*\(\s*ARRAY\s*\[([^]]*)\]\s*\)", re.I)
    all_re = re.compile(r"([\"A-Za-z_0-9.]+(?:\([^()]*\))?)\s*<>\s*ALL\s*\(\s*ARRAY\s*\[([^]]*)\]\s*\)", re.I)
    pos_re = re.compile(
        r"POSITION\s*\(\s*('(?:[^']|'')*'|\([^()]*\)|[\w.]+)\s+IN\s+('(?:[^']|'')*'|\([^()]*\)|[\w.]+)\s*\)",
        re.I)
    call_re = re.compile(r"\b([a-z_][a-z_0-9]*)\s*\(", re.I)
    # QA 复审白名单：SQLite 标量函数面
    SQLITE_FUNCS = {"length", "instr", "substr", "substring", "lower", "upper", "trim",
                    "ltrim", "rtrim", "replace", "coalesce", "abs", "min", "max", "round",
                    "json_extract", "json_type", "json_array_length", "json_valid",
                    "json_quote", "ifnull", "nullif", "typeof"}
    SQL_KEYWORDS = {"in", "not", "exists", "select", "case", "when", "then", "else", "end",
                    "and", "or", "between", "is", "null", "like", "glob", "cast", "check"}
    pg_only = re.compile(r"~~|::~|!~|\s~+\s|::[a-z_]|jsonb_|to_tsvector|statement_timestamp|"
                         r"encode\s*\(|sha256|make_interval|interval '|current_setting\s*\(|"
                         r"<@|@>|\bARRAY\s*\[", re.I)


    def rewrite_call(text, name, build):
        # 平衡括号替换：name(EXPR) → build(EXPR)
        out = []
        i = 0
        pat = re.compile(r"\b" + name + r"\s*\(", re.I)
        while True:
            m = pat.search(text, i)
            if not m:
                out.append(text[i:])
                break
            out.append(text[i:m.start()])
            depth = 1
            j = m.end()
            while j < len(text) and depth:
                if text[j] == "(":
                    depth += 1
                elif text[j] == ")":
                    depth -= 1
                j += 1
            inner = text[m.end():j - 1]
            out.append(build(inner))
            i = j
        return "".join(out)

    def normalize(expr):
        e = num_cast_re.sub(lambda m: m.group(1), expr)
        e = cast_re.sub(lambda m: "'" + m.group(1) + "'", e)
        e = re.sub(r"\bbtrim\s*\(", "trim(", e, flags=re.I)
        e = rewrite_call(e, "octet_length", lambda inner: f"length(CAST({inner} AS BLOB))")
        e = rewrite_call(e, "pg_column_size", lambda inner: f"length(CAST({inner} AS BLOB))")
        e = rewrite_call(e, "cardinality", lambda inner: f"json_array_length({inner})")
        e = re.sub(r"jsonb_typeof\s*\(", "json_type(", e, flags=re.I)
        e = re.sub(r"jsonb_array_length\s*\(", "json_array_length(", e, flags=re.I)
        def strip_outer(x):
            x = x.strip()
            while x.startswith("(") and x.endswith(")"):
                depth = 0
                ok = True
                for i, ch in enumerate(x):
                    if ch == "(":
                        depth += 1
                    elif ch == ")":
                        depth -= 1
                        if depth == 0 and i != len(x) - 1:
                            ok = False
                            break
                if not ok:
                    break
                x = x[1:-1].strip()
            return x
        e = pos_re.sub(lambda m: f"instr({strip_outer(m.group(2))}, {strip_outer(m.group(1))})", e)
        while True:
            e2 = any_re.sub(lambda m: f"{m.group(1)} IN ({m.group(2)})", e)
            e3 = all_re.sub(lambda m: f"{m.group(1)} NOT IN ({m.group(2)})", e2)
            if e3 == e:
                break
            e = e3
        return e

    def pg_untranslatable(expr):
        if pg_only.search(expr):
            return True
        for m in call_re.finditer(expr):
            name = m.group(1).lower()
            if name in SQL_KEYWORDS or name in SQLITE_FUNCS:
                continue
            return True
        return False

    # CHECK：规范化后仍不可译 → 剥离登记
    stripped_checks = []
    normalized_checks = {}
    for t, ks in checks.items():
        for k in ks:
            body = normalize(k["def"])
            if pg_untranslatable(body):
                if "sha256" in body.lower() or "encode(" in body.lower():
                    risk = "digest 完整性校验缺失：可能写入与字段不匹配的摘要"
                elif "~" in body:
                    risk = "文本模式校验缺失：可能写入非法格式值"
                else:
                    risk = "语义校验缺失：应用层需等价断言"
                stripped_checks.append({"table": t, "name": k["name"], "def": k["def"],
                                         "risk": risk, "slice": "S4",
                                         "reason": "CHECK 内调函数/PG 语义不可译——S4 应用层等价断言承接"})
            else:
                normalized_checks.setdefault(t, []).append(
                    "CHECK " + body[body.index("("):].rstrip(","))

    # 默认值：now() 类翻译为 unix 微秒表达式；纯字面量保留；其余逐条登记。
    # 断言：PG 有默认值而 SQLite 未生成默认值的列，必须全部出现在登记表
    # （没有静默丢失的默认值）。
    simple_default = re.compile(r"^'([^']|'')*'$|^-?[0-9]+$|^-?[0-9]+\.[0-9]+$")
    now_plus_re = re.compile(
        r"^\(?\s*(now\(\)|current_timestamp)\s*\+\s*'"
        r"(?:(\d+)\s*days?|(\d+):(\d+):(\d+))'\s*(?:::interval)?\s*\)?$", re.I)
    now_default_re = re.compile(
        r"^(now\(\)|current_timestamp|statement_timestamp\(\)|transaction_timestamp\(\)|clock_timestamp\(\))(::timestamp with time zone|::timestamp without time zone|::timestamptz|::timestamp)?$",
        re.I)
    translated_defaults = {}
    stripped_defaults = []
    pg_default_cols = 0
    for t, cs in by_table_cols.items():
        for c in cs:
            d = c["default"].strip()
            if not d and c["identity"] not in ("a", "d"):
                continue
            pg_default_cols += 1
            if d.startswith("nextval(") or c["identity"] in ("a", "d"):
                continue  # AUTOINCREMENT / identity 由 PK 处理
            norm = normalize(d)
            if norm in ("false", "true"):
                # PG boolean literal default → SQLite INTEGER 0/1.
                translated_defaults[(t, c["column"])] = "0" if norm == "false" else "1"
                continue
            if re.match(r"^'\{\}'(?:\s*::\s*[a-z_ ]+(?:\[\])?)?$", d):
                # PG 空数组默认 → JSON 数组编码（v3.2 定死：text[]/bigint[] 存 JSON）。
                translated_defaults[(t, c["column"])] = "'[]'"
                continue
            m_plus = now_plus_re.match(norm.strip())
            if m_plus:
                if m_plus.group(2):
                    offset = int(m_plus.group(2)) * 86400
                else:
                    offset = int(m_plus.group(3)) * 3600 + int(m_plus.group(4)) * 60 + int(m_plus.group(5))
                translated_defaults[(t, c["column"])] = (
                    f"((CAST(strftime('%s','now') AS INTEGER)*1000000) + {offset}*1000000)")
                continue
            if now_default_re.match(norm):
                translated_defaults[(t, c["column"])] = "(CAST(strftime('%s','now') AS INTEGER)*1000000)"
                continue
            if simple_default.match(norm):
                translated_defaults[(t, c["column"])] = norm
                continue
            stripped_defaults.append({"table": t, "column": c["column"], "default": d,
                                       "reason": "默认值含不可译函数/表达式——SQLite 侧由应用层供给"})
    emitted_defaults = len(translated_defaults)
    registered = len(stripped_defaults)
    serial_default_cols = 0
    for t, cs in by_table_cols.items():
        for c in cs:
            d = c["default"].strip()
            if d.startswith("nextval(") or c["identity"] in ("a", "d"):
                serial_default_cols += 1
    if pg_default_cols != emitted_defaults + registered + serial_default_cols:
        print(f"DEFAULT ACCOUNTING DRIFT: pg={pg_default_cols} emitted={emitted_defaults} "
              f"registered={registered} serial={serial_default_cols}")
        sys.exit(1)

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

    # serial 判定（QA P0-1 收紧）：单列主键 + PG 类型 ∈ {bigint, integer} +
    # （identity in ('a','d') 或默认 nextval）。其余单列主键一律表级
    # PRIMARY KEY（TEXT/UUID/BOOLEAN 主键绝不能变成 rowid 别名，否则插入即
    # datatype mismatch，且 tenant_id 缺失会被静默分配 id）。
    def is_serial_pk(table, col_name):
        for c in by_table_cols[table]:
            if c["column"] == col_name:
                if c["type"] not in ("bigint", "integer"):
                    return False
                return c["identity"] in ("a", "d") or c["default"].strip().startswith("nextval(")
        return False

    autoincrement_cols = []
    for t in tables:
        cs = by_table_cols[t]
        pk = pks.get(t, [])
        pk_cols = []
        for k in pk:
            m = re.search(r"PRIMARY KEY\s*\(([^)]*)\)", k["def"])
            if m:
                pk_cols = [x.strip().strip('"') for x in m.group(1).split(",")]
        single_serial_pk = None
        if len(pk_cols) == 1 and is_serial_pk(t, pk_cols[0]):
            single_serial_pk = pk_cols[0]
            autoincrement_cols.append(f"{t}.{single_serial_pk}")
            col_type = next(c["type"] for c in cs if c["column"] == single_serial_pk)
            if col_type not in ("bigint", "integer"):
                print(f"AUTOINCREMENT ASSERTION FAILED: {t}.{single_serial_pk} is {col_type}")
                sys.exit(1)

        lines = []
        for c in cs:
            sqlite, _ = map_type(c["type"])
            if c["column"] == single_serial_pk:
                lines.append(f"  {quote(c['column'])} INTEGER PRIMARY KEY AUTOINCREMENT")
                continue
            piece = f"  {quote(c['column'])} {sqlite}"
            if c["notnull"]:
                piece += " NOT NULL"
            if (t, c["column"]) in translated_defaults:
                piece += f" DEFAULT {translated_defaults[(t, c['column'])]}"
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
               "dropped_indexes": dropped_indexes,
               "autoincrement_columns": autoincrement_cols,
               "type_map_notes": {"numeric": "本 schema 0 列——映射 TEXT 定点规则预置但不适用（与 citext 同）",
                                  "citext": "本 schema 0 列——映射 NOCASE 规则预置但不适用"}},
              open(args.registry_out, "w"), indent=1, ensure_ascii=False)
    with open(args.type_map_out, "w") as f:
        def encoding(pg_type):
            if pg_type.endswith("[]"):
                return "JSON array text (v3.2: PG array → JSON; '[]' 空值；迁移器同编码转换)"
            if pg_type in ("jsonb", "json"):
                return "JSON text"
            if pg_type.startswith("timestamp"):
                return "unix micro"
            return "-"
        f.write("table\tcolumn\tpg_type\tsqlite_type\tencoding\n")
        for row in type_rows:
            f.write("\t".join(row) + "\t" + encoding(row[2]) + "\n")
    print(json.dumps({"tables": len(tables) + 3, "autoincrement": len(autoincrement_cols),
                      "defaults_emitted": emitted_defaults, "defaults_registered": registered,
                      "type_counts": type_counts,
                      "stripped_checks": len(stripped_checks),
                      "stripped_defaults": len(stripped_defaults),
                      "dropped_indexes": len(dropped_indexes),
                      "kept_indexes": len(kept_indexes)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
