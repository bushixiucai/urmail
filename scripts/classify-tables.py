#!/usr/bin/env python3
"""classify-tables.py — Phase 1 表分类单一事实源。

权威输入是「migrations 0..HEAD 全量应用后的 PG catalog dump」（TSV，段：
TABLE/POLICY/TRIGGER/FUNCTION/GRANT），由 internal/testdb 的临时 catalog
dump helper 产出（见 data/pg-catalog-20261119.tsv 的取证副本）。migrations
目录只作静态交叉核对（CREATE − DROP == catalog 活表集），任何漂移即失败。

用法：
  classify-tables.py --catalog data/pg-catalog-20261119.tsv \
                    --migrations ../../../../../dev/urMail.cn/mailinabox/migrations \
                    --out-dir .
  classify-tables.py ... --check            # 与 golden tables.json 比对，漂移退出 1（CI 门）
  classify-tables.py ... --dump-trigger-port   # 打印 60 个触发器移植清单底表

输出：tables.json（machine-readable，矩阵生成与 lint 的输入）、appendix.md、
counts 摘要到 stdout。
"""
import argparse, glob, json, os, re, sys

# ---- 人工判定层（可审计、随 S0 复核更新；其余全部由 catalog 机判） ----
CHAIN = {
    # 无 tenant_id 但行属单一租户（经 FK 链）
    "mailbox_storage_reservations": "address→mailboxes 链路（触发器维护，平台写）",
}
# 多作用域表（S0-a 复核改判）：行谓词 = scope/scope_id，租户行与平台/品牌
# 行同表共存——矩阵按行谓词覆盖，不按整表归类。
MULTISCOPE = {
    "settings": "多作用域（scope=platform/brand/tenant，行谓词 scope/scope_id；"
                "settings.sql 有租户域 Get/Upsert/DeleteTenantSetting 与品牌域写路径，共 7 条写语句）",
}
SHARED_NOTE = {
    "users": "跨租户主体（user_tenant_roles 多租户映射）",
    "tenants": "租户注册表（平台管理）",
    "platform_admins": "平台管理员",
    "announcements": "平台广播",
    "channels": "平台频道",
    "brands": "平台品牌注册表",
    "brand_hosts": "平台品牌域",
    "brand_host_challenges": "平台验证挑战",
    "application_credentials": "平台出站凭据（0020）",
    "backup_runs": "备份运维状态（0120）",
    "collector_checkpoints": "采集位点（0038）",
    "delivery_queue_contexts": "投递队列上下文（0038）",
    "dns_zone_versions": "DNS 区域版本（平台）",
    "restore_quarantine": "恢复隔离标记",
    "roundcube_bridge_requests": "roundcube 桥接请求（平台）",
}
# 登记撤销的遗留 GRANT（S0-c 复核）：queries 无任何对应写路径，矩阵的 app
# 写轴不得凭空计入；S1 迁移执行 REVOKE 落地撤销。
REGISTERED_GRANT_REVOCATIONS = {
    "platform_admins": "urmail_app DELETE+INSERT+UPDATE 无对应写路径（queries 零命中）——遗留 GRANT，登记撤销，S1 执行 REVOKE",
    "restore_quarantine": "urmail_app DELETE+INSERT+UPDATE 无对应写路径（queries 零命中）——遗留 GRANT，登记撤销，S1 执行 REVOKE",
}
# settings 的 app 写授权有真实写路径（tenant/brand 域 7 条写语句），不撤销。
# 语义 append-only（现 PG 用 RESTRICTIVE/REVOKE 表达；SQLite 触发器补硬约束）
APPEND_ONLY = ["audit_logs", "announcements", "channels", "ai_usage_requests", "delivery_events"]
# 废弃函数登记（QA 复核：死表宿主或零调用死函数，迁移即清理）
DEPRECATED_FUNCTIONS = {"revoke_renewal_on_subscription_cancel":
                        "0138 建于已 DROP 的 subscriptions 上，全仓无引用，废弃并随迁移清理"}
# CI 门钉子：catalog 口径漂移即红（QA 实测；v3 起函数口径=业务函数，扩展
# pg_trgm/plpgsql 自有 31 个函数被 EXTENSION 段排除，150-31=119；
# S0-a 后 shared=15 + shared_multiscope=1（settings 改判多作用域））
PINNED = {"business_tables": 102, "tenant_direct": 85, "rls": 101, "force": 43,
          "restrictive_tables": 4, "restrictive_policies": 10, "shared": 15,
          "shared_multiscope": 1, "triggers": 60, "business_functions": 119}


def load_catalog(path):
    tables, policies, triggers, functions, extensions, grants = {}, [], [], [], [], []
    section = None
    with open(path) as f:
        for line in f:
            cols = line.rstrip("\n").split("\t")
            if cols[0] in ("TABLE", "POLICY", "TRIGGER", "FUNCTION", "GRANT", "EXTENSION"):
                if cols[0] == "TABLE" and cols[1] == "tenant_id":  # header
                    continue
                if cols[0] == "POLICY" and cols[1] == "table":
                    continue
                if cols[0] == "TRIGGER" and cols[1] == "table":
                    continue
                if cols[0] == "FUNCTION" and cols[1] == "name":
                    continue
                if cols[0] == "GRANT" and cols[1] == "table":
                    continue
                if cols[0] == "EXTENSION" and cols[1] == "name":
                    continue
                section = cols[0]
                cols = cols[1:]
            if section == "TABLE":
                tables[cols[0]] = {"tenant": cols[1] == "tenant_id",
                                   "rls": cols[2] == "true", "force": cols[3] == "true"}
            elif section == "POLICY":
                policies.append({"table": cols[0], "name": cols[1],
                                 "permissive": cols[2], "cmd": cols[3], "roles": cols[4]})
            elif section == "TRIGGER":
                triggers.append({"table": cols[0], "name": cols[1],
                                 "function": cols[2], "enabled": cols[3]})
            elif section == "FUNCTION":
                functions.append(cols[0])
            elif section == "EXTENSION":
                extensions.append(cols[0])
            elif section == "GRANT":
                grants.append({"table": cols[0], "role": cols[1], "privs": cols[2].split(",")})
    return tables, policies, triggers, functions, extensions, grants


def static_cross_check(migrations_dir, live):
    if not migrations_dir:
        return []
    created, dropped = set(), set()
    for f in sorted(glob.glob(os.path.join(migrations_dir, "*.up.sql"))):
        sql = open(f).read()
        created |= set(re.findall(r"CREATE TABLE (?:IF NOT EXISTS )?([a-z_0-9]+)\s*\(", sql))
        dropped |= set(re.findall(r"DROP TABLE (?:IF EXISTS )?([a-z_0-9]+)", sql))
    drift = []
    static_live = created - dropped
    for t in sorted(static_live - live):
        drift.append(f"static-has-catalog-missing: {t}")
    for t in sorted(live - static_live):
        drift.append(f"catalog-has-static-missing: {t}")
    return drift


def main():
    default_out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "isolation")
    ap = argparse.ArgumentParser()
    ap.add_argument("--catalog", required=True)
    ap.add_argument("--migrations", default="")
    ap.add_argument("--out-dir", default=default_out,
                    help="default: scripts/isolation next to this script")
    ap.add_argument("--check", action="store_true",
                    help="read-only golden compare: builds everything in memory, writes nothing")
    ap.add_argument("--dump-trigger-port", action="store_true")
    args = ap.parse_args()

    tables, policies, triggers, functions, extensions, grants = load_catalog(args.catalog)
    live = set(tables) - {"schema_migrations"}
    business = set(t for t in live if t != "schema_migrations")

    # 静态交叉核对
    drift = static_cross_check(args.migrations, business)
    if drift:
        print("STATIC/CATALOG DRIFT:")
        for d in drift:
            print(" ", d)
        sys.exit(1)

    restrictive_tables = sorted({p["table"] for p in policies if p["permissive"] == "RESTRICTIVE"})
    restrictive_policies = sum(1 for p in policies if p["permissive"] == "RESTRICTIVE")
    app_grants = {g["table"]: set(g["privs"]) for g in grants if g["role"] == "urmail_app"}

    rows = {}
    for t in sorted(business):
        info = tables[t]
        if info["tenant"]:
            cat, basis = "tenant", "tenant_id 列"
        elif t in CHAIN:
            cat, basis = "tenant(chain)", CHAIN[t]
        elif t in MULTISCOPE:
            cat, basis = "shared-multiscope", MULTISCOPE[t]
        else:
            cat, basis = "shared", SHARED_NOTE.get(t, "无 tenant_id（平台域）")
        tags = []
        if info["rls"]:
            tags.append("RLS")
        if info["force"]:
            tags.append("FORCE")
        if t in restrictive_tables:
            tags.append("RESTRICTIVE")
        if t in APPEND_ONLY:
            tags.append("append-only")
            # QA P1-3c：现对 urmail_app 非 INSERT 授权的 append-only 表，
            # SQLite 去权限后必须补 no_insert 触发器防权限扩大。
            if not (app_grants.get(t, set()) & {"INSERT", "ALL"}):
                tags.append("needs-no-insert-trigger")
        if t in REGISTERED_GRANT_REVOCATIONS:
            # S0-c：遗留 GRANT 登记撤销——矩阵的 app 写轴不得计入。
            tags.append("grant-revocation-registered")
        rows[t] = {"class": cat, "basis": basis, "tags": tags,
                   "app_privs": sorted(app_grants.get(t, [])),
                   "rls": info["rls"], "force": info["force"]}

    counts = {
        "business_tables": len(business),
        "tenant_direct": sum(1 for r in rows.values() if r["class"] == "tenant"),
        "rls": sum(1 for r in rows.values() if r["rls"]),
        "force": sum(1 for r in rows.values() if r["force"]),
        "restrictive_tables": len(restrictive_tables),
        "restrictive_policies": restrictive_policies,
        "shared": sum(1 for r in rows.values() if r["class"] == "shared"),
        "shared_multiscope": sum(1 for r in rows.values() if r["class"] == "shared-multiscope"),
        "triggers": len(triggers),
        # v3 口径：业务函数「条目」数（EXTENSION 段排除扩展函数；含同名
        # 重载条目，如 urmail_record_audit 双签名——distinct 名为 118）
        "business_functions": len(functions),
    }
    for k, v in PINNED.items():
        if counts[k] != v:
            print(f"PIN DRIFT: {k} = {counts[k]}, pinned {v}")
            sys.exit(1)

    if args.dump_trigger_port:
        for tr in triggers:
            print(f"TRIGGER-PORT\t{tr['table']}\t{tr['name']}\t{tr['function']}\t{tr['enabled']}")
        for fn, note in DEPRECATED_FUNCTIONS.items():
            if fn in set(functions):
                print(f"FUNCTION-DEPRECATED\t{fn}\t{note}")
        print(f"FUNCTIONS-BUSINESS\t{len(functions)}")  # 条目口径（含重载）；distinct 名 118
        print("EXTENSIONS\t" + ",".join(extensions))
        return

    payload = json.dumps({"counts": counts, "restrictive_tables": restrictive_tables,
                          "append_only": APPEND_ONLY, "tables": rows,
                          "registered_grant_revocations": REGISTERED_GRANT_REVOCATIONS,
                          "triggers": triggers,
                          "extensions": extensions,
                          "deprecated_functions": DEPRECATED_FUNCTIONS,
                          "functions_live": sorted(set(functions))}, indent=1, ensure_ascii=False)

    if args.check:
        # Pure-read gate: build in memory, compare against the golden, and
        # write NOTHING — a clean checkout stays clean (golden is seeded
        # only by a reviewed non-check run, never by CI).
        golden = os.path.join(args.out_dir, "tables.golden.json")
        if not os.path.exists(golden):
            print(f"golden missing: {golden} (seed it in a reviewed run, never in CI)")
            sys.exit(1)
        if open(golden).read() != payload:
            print("GOLDEN DRIFT: regenerate and review before landing")
            sys.exit(1)
        print(json.dumps(counts, ensure_ascii=False))
        return

    os.makedirs(args.out_dir, exist_ok=True)
    with open(os.path.join(args.out_dir, "tables.json"), "w") as f:
        f.write(payload)
    with open(os.path.join(args.out_dir, "appendix.md"), "w") as f:
        f.write("| 表名 | 类别 | 依据 | 标签 | urmail_app 授权 |\n|---|---|---|---|---|\n")
        for t in sorted(rows):
            r = rows[t]
            f.write(f"| {t} | {r['class']} | {r['basis']} | {', '.join(r['tags']) or '—'} |"
                    f" {', '.join(r['app_privs']) or '无'} |\n")
    print(json.dumps(counts, ensure_ascii=False))


if __name__ == "__main__":
    main()
