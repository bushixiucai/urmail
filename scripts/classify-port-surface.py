#!/usr/bin/env python3
"""classify-port-surface.py — Phase 1 S0 移植面三分类底表生成器。

输入：PG16 catalog dump（internal/testdb 的 TestDumpCatalog 产出，TSV）。
输出：scripts/isolation/port-inventory.{json,md}——119 个函数条目（118 个
distinct 名，urmail_record_audit 双签名重载）与 60 个触发器逐条分类，每项带
调用证据与替代物。

三分类（v3 §3.4 契约）：
  sqlite-trigger  行级守卫，SQLite 触发器等价重写
  app-assume      跨行/副作用/函数链，应用层或迁移器承担
  deprecated      宿主表已 DROP（32 张商业化移除表），随迁移清理

设计门：业务函数条目 119 全部分类（distinct 118 全覆盖）；60 触发器全部
映射宿主函数分类；deprecated 名单在 *.go 代码零命中（--check-deprecated）。
"""
import argparse, glob, json, os, re, sys

# ---- 死表宿主（32 张已 DROP 表，QA/v2 复核）→ 宿主函数废弃 ----
DEPRECATED = {
    "revoke_renewal_on_subscription_cancel": "subscriptions",
    "urmail_freeze_subscription_plan": "subscriptions",
    "urmail_subscription_guard": "subscriptions",
    "urmail_migration_subscription_guard": "subscriptions",
    "urmail_subscription_term_guard": "subscription_terms",
    "urmail_billing_calendar_guard": "billing_calendars",
    "urmail_coupon_reissue_immutable": "coupons",
    "urmail_coupon_release_authority": "coupon_redemptions",
    "urmail_invoice_document_immutable": "invoices",
    "urmail_order_intention_guard": "orders",
    "urmail_payment_inbox_alert": "payment_webhook_inbox",
    "urmail_record_rejected_webhook": "payment_webhook_rejections",
    "urmail_plan_snapshot_fits": "plans",
    "urmail_migration_plan_guard": "plans",
    "urmail_migration_entitlement_allowed": "plans",
}

# ---- 行级守卫（单行 NEW/OLD 校验 + RAISE，SQLite 触发器等价重写） ----
SQLITE_TRIGGER = {
    "domains_touch_updated_at": "BEFORE UPDATE 触碰 updated_at",
    "urmail_ai_configuration_consent_guard": "非 platform 租户 consent 禁改",
    "urmail_alias_sender_logins_guard": "sender_logins 身份不可变",
    "urmail_archive_purge_guard": "legal_hold 期间禁删/改",
    "urmail_billing_profile_write_guard": "billing 写权限守卫",
    "urmail_contact_group_member_guard": "成员域守卫",
    "urmail_domain_provisioning_guard": "provisioning 状态守卫",
    "urmail_guard_host_namespace": "hostname 命名空间守卫",
    "urmail_guard_marketing_snapshot": "快照不可变守卫",
    "urmail_guard_nonplatform_ai_quota": "nonplatform AI 配额守卫",
    "urmail_guard_published_marketing_step": "已发布步骤守卫",
    "urmail_hold_guard": "hold 守卫（domains/mailbox 家族复用）",
    "urmail_mailbox_identity_guard": "邮箱身份不可变守卫",
    "urmail_mailbox_password_version_guard": "密码版本守卫",
    "urmail_mailbox_sieve_mode_guard": "sieve 模式守卫",
    "urmail_recall_guard": "recall 状态守卫",
    "urmail_recall_message_identity_guard": "recall 消息身份守卫",
    "urmail_require_content_owner": "owner 角色要求",
    "urmail_require_encrypted_totp": "TOTP 加密要求",
    "urmail_require_grant_independence": "grant 独立性要求",
    "urmail_require_marketing_admin": "marketing admin 要求",
    "urmail_require_open_ticket": "open ticket 要求",
    "urmail_reserve_mailbox_storage": "存储预约（预约行写入+核对，SQLite 触发器可等价）",
    "urmail_resource_guard": "资源域守卫（多表复用）",
    "urmail_restore_quarantine_guard": "恢复隔离守卫",
    "urmail_retention_confirmation_guard": "保留确认守卫",
    "urmail_retention_management_guard": "保留管理守卫",
    "urmail_suppression_no_weaken": "suppression 不可弱化",
    "urmail_setting_limit": "settings 上限守卫（0158 guard 链）",
    "urmail_require_tenant_content_admin": "content admin 要求（0028 guard 链）",
}

# 其余全部 app-assume：有 .go/.sql/dovecot 证据，或库内函数链
# （archive_case/marketing_limits/dns_actor/evidence 存储等）。
APP_ASSUME_NOTE = "跨行/副作用/函数链 → 应用层或迁移器承担"


def load_catalog(path):
    funcs, trig = [], []
    sec = None
    for line in open(path):
        c = line.rstrip("\n").split("\t")
        if c[0] in ("TABLE", "POLICY", "TRIGGER", "FUNCTION", "EXTENSION", "GRANT"):
            if (c[0] == "FUNCTION" and c[1] == "name") or (c[0] == "TRIGGER" and c[1] == "table"):
                continue
            sec = c[0]
            c = c[1:]
        if sec == "FUNCTION":
            funcs.append(c[0])
        elif sec == "TRIGGER":
            trig.append({"table": c[0], "name": c[1], "function": c[2], "enabled": c[3]})
    return funcs, trig


def gather_evidence(repo, fn):
    ev = []
    pat = re.compile(r"\b" + re.escape(fn) + r"\b")
    roots = ["internal", "cmd", "scripts", "installer"]
    for root in roots:
        for f in glob.glob(os.path.join(repo, root, "**"), recursive=True):
            if not os.path.isfile(f) or f.endswith("_test.go") or "/dev/" in f:
                continue
            if not f.endswith((".go", ".sql", ".py", ".sh")):
                continue
            # S0 tooling itself carries the deprecated lists — it is not
            # "code referencing" anything. Exclude the classifiers and
            # everything under scripts/isolation (generated inventories).
            rel = os.path.relpath(f, repo)
            base = os.path.basename(f)
            if rel.startswith("scripts/isolation/") or base.startswith("classify-"):
                continue
            try:
                if pat.search(open(f, errors="ignore").read()):
                    ev.append(rel)
            except OSError:
                continue
    return sorted(set(ev))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--catalog", required=True)
    ap.add_argument("--repo", default=".")
    ap.add_argument("--out-dir", default="scripts/isolation")
    ap.add_argument("--check-deprecated", action="store_true",
                    help="断言 deprecated 名单在仓库 *.go 代码零命中（CI 门）")
    ap.add_argument("--self-test-deprecated", action="store_true",
                    help="在 internal/ 植入 decoy 引用并断言能被检出（证明排除规则没有把门挖空）")
    args = ap.parse_args()

    funcs, trig = load_catalog(args.catalog)
    entries, distinct = len(funcs), len(set(funcs))
    if (entries, distinct) != (119, 118):
        print(f"COUNT DRIFT: entries={entries} distinct={distinct}, want 119/118")
        sys.exit(1)

    inv = {}
    for fn in sorted(set(funcs)):
        if fn in DEPRECATED:
            inv[fn] = {"class": "deprecated", "host": DEPRECATED[fn],
                       "replacement": "随宿主死表废弃，迁移清理（v3 §3.4 废弃登记）"}
        elif fn in SQLITE_TRIGGER:
            inv[fn] = {"class": "sqlite-trigger", "note": SQLITE_TRIGGER[fn],
                       "replacement": "SQLite 触发器等价重写（行级 NEW/OLD 校验）"}
        else:
            inv[fn] = {"class": "app-assume", "note": APP_ASSUME_NOTE,
                       "replacement": "应用层/迁移器承担；库内函数链者在 S1 逐条内联"}
        inv[fn]["overloads"] = funcs.count(fn)
        if fn not in DEPRECATED or args.check_deprecated is False:
            pass
    # 调用证据（deprecated 也要证据=零命中断言的输入）
    for fn in inv:
        inv[fn]["evidence"] = gather_evidence(args.repo, fn)

    # 触发器逐条映射宿主函数分类
    for t in trig:
        t["class"] = inv[t["function"]]["class"]
    if len(trig) != 60:
        print(f"TRIGGER COUNT DRIFT: {len(trig)}, want 60")
        sys.exit(1)

    # 设计门：119 条目全覆盖（distinct 118 全分类即覆盖条目）
    classified = {c: sum(1 for f in inv.values() if f["class"] == c)
                  for c in ("sqlite-trigger", "app-assume", "deprecated")}
    if sum(classified.values()) != distinct:
        print("CLASSIFICATION INCOMPLETE")
        sys.exit(1)

    if args.self_test_deprecated:
        decoy = os.path.join(args.repo, "internal", "zz_deprecated_selftest.go")
        fn = next(iter(DEPRECATED))
        try:
            with open(decoy, "w") as f:
                f.write(f"package x\n\n// gate self-test decoy: {fn}\n")
            hits = {name: gather_evidence(args.repo, name) for name in DEPRECATED}
            if not hits.get(fn):
                print("SELF-TEST FAIL: decoy not detected — exclusion rules hollowed out the gate")
                sys.exit(1)
            if any(hits.get(n) for n in DEPRECATED if n != fn):
                print(f"SELF-TEST FAIL: unexpected extra hits: {hits}")
                sys.exit(1)
        finally:
            os.remove(decoy)
        print(f"deprecated gate self-test OK (decoy {fn} detected; tooling excluded)")
        return

    if args.check_deprecated:
        hits = {fn: inv[fn]["evidence"] for fn in DEPRECATED if inv[fn]["evidence"]}
        if hits:
            print("DEPRECATED REFERENCE HITS:")
            for fn, ev in hits.items():
                print(f"  {fn}: {ev}")
            sys.exit(1)
        print(f"deprecated zero-hit OK ({len(DEPRECATED)} names)")
        return

    os.makedirs(args.out_dir, exist_ok=True)
    with open(os.path.join(args.out_dir, "port-inventory.json"), "w") as f:
        json.dump({"counts": {"entries": entries, "distinct": distinct,
                              "triggers": len(trig), **classified},
                   "functions": inv, "triggers": trig}, f, indent=1, ensure_ascii=False)
    with open(os.path.join(args.out_dir, "port-inventory.md"), "w") as f:
        f.write(f"# 移植面三分类底表（S0，脚本生成）\n\n"
                f"函数条目 {entries}（distinct {distinct}，urmail_record_audit 双签名）；"
                f"触发器 {len(trig)}。\n\n"
                f"分类计数：{json.dumps(classified, ensure_ascii=False)}\n\n"
                "| 函数 | 类别 | 替代物/依据 | 调用证据 |\n|---|---|---|---|\n")
        for fn in sorted(inv):
            r = inv[fn]
            f.write(f"| {fn} | {r['class']} | {r.get('host') or r.get('note') or ''} |"
                    f" {', '.join(r['evidence']) or '库内（迁移/触发器链）'} |\n")
        f.write("\n| 触发器 | 表 | 宿主函数 | 分类 |\n|---|---|---|---|\n")
        for t in trig:
            f.write(f"| {t['name']} | {t['table']} | {t['function']} | {t['class']} |\n")
    print(json.dumps({"entries": entries, "distinct": distinct,
                      "triggers": len(trig), **classified}, ensure_ascii=False))


if __name__ == "__main__":
    main()
