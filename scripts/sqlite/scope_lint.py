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
    args = ap.parse_args()

    golden = json.load(open(args.golden))
    tenant_tables = {t for t, v in golden["tables"].items()
                     if v["class"] in ("tenant", "tenant(chain)")}
    platform_methods = set()
    if os.path.exists(args.platform_methods):
        platform_methods = {l.strip() for l in open(args.platform_methods)
                            if l.strip() and not l.startswith("#")}

    violations = []
    unbound = []
    scopes = {}
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
            sm = re.search(r"(?m)^-- scope:\s*(tenant|platform)\s*$", body)
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
                unbound.append(name)

    if violations:
        print(f"SCOPE LINT VIOLATIONS = {len(violations)}:")
        for v in violations[:20]:
            print(" ", v)
        sys.exit(1)

    counts = {"tenant": sum(1 for s in scopes.values() if s == "tenant"),
              "platform": sum(1 for s in scopes.values() if s == "platform")}
    if args.write_scopes:
        json.dump({"scopes": scopes, "counts": counts,
                   "tenant_id_unbound": sorted(unbound)},
                  open(args.out, "w"), indent=1, ensure_ascii=False)
        print(json.dumps({"statements": total, "scopes": counts,
                          "tenant_id_unbound": len(unbound),
                          "method_scopes": args.out}, ensure_ascii=False))
    else:
        print(json.dumps({"statements": total, "scopes": counts,
                          "tenant_id_unbound": len(unbound)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
