#!/usr/bin/env python3
"""scope_lint.py — S2 门：每条语句必有 -- scope 注解，且注解与表触达一致。

规则（v3.2 §2.2 / S2）：
  - 每个 `-- name: X` 语句下必须有 `-- scope: tenant|platform`；
  - scope=platform 且触及 tenant 类表 → 必须与 platform-methods.txt 豁免
    清单一致（清单可审计增删）；
  - scope=tenant 且触达 tenant 类表但全文无 tenant_id 绑定 → 记为
    tenant_id-unbound（信息级，S3 装饰器按方法作用域表兜底）。

同时生成 method_scopes.json（注解 → 方法作用域），供 S2 末编译期断言消费。
退出码：违例=1；否则 0 并打印计数。
"""
import argparse, glob, json, os, re, sys


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", default="internal/store/queries")
    ap.add_argument("--golden", default="scripts/isolation/tables.golden.json")
    ap.add_argument("--platform-methods", default="scripts/sqlite/platform-methods.txt")
    ap.add_argument("--out", default="scripts/sqlite/method_scopes.json")
    ap.add_argument("--write-scopes", action="store_true")
    ap.add_argument("--handwritten-scopes",
                    default="scripts/sqlite/handwritten-scopes.txt",
                    help="S4 手写层（sqlite_methods.go）方法作用域，按行登记")
    ap.add_argument("--audit-writers",
                    default="scripts/sqlite/audit-write-paths.txt",
                    help="S6 应用层写集守卫：audit_logs INSERT 路径白名单")
    args = ap.parse_args()

    golden = json.load(open(args.golden))
    tenant_tables = {t for t, v in golden["tables"].items()
                     if v["class"] in ("tenant", "tenant(chain)")}
    platform_methods = set()
    if os.path.exists(args.platform_methods):
        platform_methods = {l.strip() for l in open(args.platform_methods)
                            if l.strip() and not l.startswith("#")}

    handwritten = {}
    if os.path.exists(args.handwritten_scopes):
        for line in open(args.handwritten_scopes):
            line = line.split("#", 1)[0].strip()
            if not line:
                continue
            parts = line.split()
            if len(parts) != 2 or parts[1] not in ("tenant", "platform", "system", "pre-auth"):
                print(f"handwritten-scopes: bad line {line!r}")
                sys.exit(1)
            handwritten[parts[0]] = parts[1]

    violations = []
    unbound = []
    scopes = {}
    stmt_bodies = {}
    total = 0
    for path in sorted(glob.glob(os.path.join(args.dir, "*.sql"))):
        src = open(path).read()
        parts = re.split(r"(?m)^(-- name:\s*\w+.*)$", src)
        for i in range(1, len(parts), 2):
            header = parts[i]
            body = parts[i + 1] if i + 1 < len(parts) else ""
            m = re.match(r"-- name:\s*(\w+)\s*(?::\w+)?", header)
            name = m.group(1)
            total += 1
            stmt_bodies[name] = body
            sm = re.search(r"(?m)^-- scope:\s*(tenant|platform|system|pre-auth)\s*$", body)
            if not sm:
                violations.append(f"{name} ({os.path.basename(path)}): missing -- scope")
                continue
            scope = sm.group(1)
            scopes[name] = scope
            toks = set(re.findall(r"\b([a-z_]{3,})\b", body.lower()))
            touches_tenant = bool(toks & tenant_tables)
            if scope == "platform" and touches_tenant and name not in platform_methods:
                violations.append(f"{name}: platform without exemption-list entry")
            if scope == "tenant" and touches_tenant and "tenant_id" not in body.lower():
                if not re.search(r"(?m)^-- scope-reason:\s*\S", body):
                    violations.append(f"{name}: tenant-table statement without tenant_id lacks -- scope-reason")
                else:
                    unbound.append(name)

    # S6 应用层写集守卫：no_insert 触发器取消后，audit_logs 的 INSERT 只允许
    # 经过登记在案的路径（PHASE1-S2-APPEND-ONLY-AUDIT-CONFLICT-20261120.md
    # 方案①）。新增/删除写者必须同步清单，否则门禁失败；这不是「审计靠纪律」，
    # 而是把写集做成可审的闭合集合。
    audit_stmt = sorted(n for n, body in stmt_bodies.items()
                        if re.search(r"INSERT\s+INTO\s+audit_logs", body, re.I))
    audit_const = []
    go_path = os.path.join("internal", "store", "sqlite_methods.go")
    if os.path.exists(go_path):
        gosrc = open(go_path).read()
        for cm in re.finditer(r"const (\w+) = `(.*?)`", gosrc, re.S):
            if re.search(r"INSERT\s+INTO\s+audit_logs", cm.group(2), re.I):
                audit_const.append(cm.group(1))
        audit_const.sort()
    audit_trigger = []
    trigger_path = os.path.join("migrations_sqlite", "0002_write_side_effects.sql")
    if os.path.exists(trigger_path):
        tsrc = open(trigger_path).read()
        for tm in re.finditer(r"CREATE TRIGGER IF NOT EXISTS (\w+)(.*?)(?=CREATE TRIGGER|\Z)",
                              tsrc, re.S):
            if re.search(r"INSERT\s+INTO\s+audit_logs", tm.group(2), re.I):
                audit_trigger.append(tm.group(1))
        audit_trigger.sort()

    allowed = {"stmt": set(), "const": set(), "trigger": set()}
    if not os.path.exists(args.audit_writers):
        violations.append(f"missing audit writer registry {args.audit_writers}")
    else:
        for line in open(args.audit_writers):
            line = line.split("#", 1)[0].strip()
            if not line:
                continue
            kind, sep, value = line.partition(":")
            kind = kind.strip()
            value = value.strip()
            if not sep or kind not in allowed or not value:
                violations.append(f"{args.audit_writers}: bad line {line!r} "
                                  "(want stmt:NAME|const:NAME|trigger:NAME)")
                continue
            allowed[kind].add(value)
    for kind, found in (("stmt", audit_stmt), ("const", audit_const),
                        ("trigger", audit_trigger)):
        for name in found:
            if name not in allowed[kind]:
                violations.append(f"audit writer {kind}:{name} not in {args.audit_writers}")
        for name in sorted(allowed[kind]):
            if name not in found:
                violations.append(f"stale {kind}:{name} in {args.audit_writers}")

    if violations:
        print(f"SCOPE LINT VIOLATIONS = {len(violations)}:")
        for v in violations[:20]:
            print(" ", v)
        sys.exit(1)

    # 手写层合并：同名方法出现两种作用域即中止（不允许静默覆盖）。
    for name, scope in handwritten.items():
        if name in scopes and scopes[name] != scope:
            print(f"scope conflict: {name} annotated {scopes[name]}, handwritten {scope}")
            sys.exit(1)
        scopes[name] = scope

    counts = {axis: sum(1 for s in scopes.values() if s == axis)
              for axis in ("tenant", "platform", "system", "pre-auth")}
    if args.write_scopes:
        json.dump({"scopes": scopes, "counts": counts,
                   "tenant_id_unbound": sorted(unbound),
                   "audit_writers": {"statements": audit_stmt,
                                     "consts": audit_const,
                                     "triggers": audit_trigger}},
                  open(args.out, "w"), indent=1, ensure_ascii=False)
        print(json.dumps({"statements": total, "handwritten": len(handwritten),
                          "scopes": counts,
                          "tenant_id_unbound": len(unbound),
                          "audit_writers": len(audit_stmt) + len(audit_const) + len(audit_trigger),
                          "method_scopes": args.out}, ensure_ascii=False))
    else:
        print(json.dumps({"statements": total, "scopes": counts,
                          "tenant_id_unbound": len(unbound)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
