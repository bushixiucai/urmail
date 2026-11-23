#!/usr/bin/env python3
"""gen_isolation_decorator.py — generate internal/db/isolation/scoped_querier_gen.go.

Reads:
  - internal/store/querier.go (authoritative 702-method store.Querier surface)
  - internal/store/*.go non-test parameter structs (to detect TenantID fields)
  - internal/db/isolation/method_scopes.json (authoritative scope registry)

Fails loudly if any store.Querier method is missing from method_scopes.json or
uses an unknown scope, and emits `var _ store.Querier = (*ScopedQuerier)(nil)`
so any signature drift fails `go build`.

Usage:
  python3 scripts/sqlite/gen_isolation_decorator.py          # write scoped_querier_gen.go
  python3 scripts/sqlite/gen_isolation_decorator.py --check  # fail if stale or unmapped
"""
import argparse
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
QUERIER_GO = ROOT / "internal" / "store" / "querier.go"
STORE_DIR = ROOT / "internal" / "store"
SCOPES_JSON = ROOT / "internal" / "db" / "isolation" / "method_scopes.json"
PARAM_FIELDS_JSON = ROOT / "scripts" / "sqlite" / "param_tenant_actor_fields.json"
OUT_GO = ROOT / "internal" / "db" / "isolation" / "scoped_querier_gen.go"

SCOPE_CONST = {
    "tenant": "ScopeTenant",
    "platform": "ScopePlatform",
    "system": "ScopeSystem",
    "pre-auth": "ScopePreAuth",
}

BUILTIN_OR_QUALIFIED_TYPES = {
    "bool",
    "string",
    "int",
    "int32",
    "int64",
    "uint32",
    "uint64",
    "float64",
    "byte",
    "any",
    "interface{}",
    "[]byte",
    "[]string",
    "[]int64",
    "[]int32",
}


def load_store_structs() -> dict[str, dict[str, str]]:
    structs: dict[str, dict[str, str]] = {}
    for path in sorted(STORE_DIR.glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        txt = path.read_text()
        for m in re.finditer(r"(?m)^type (\w+) struct \{\n(.*?)\n\}", txt, re.S):
            fields: dict[str, str] = {}
            for line in m.group(2).splitlines():
                fm = re.match(r"^\s*(\w+)\s+(\S+)", line)
                if fm:
                    fields[fm.group(1)] = fm.group(2)
            structs[m.group(1)] = fields
    return structs


def qualify_type(t: str) -> str:
    t = t.strip()
    if t.startswith("[]"):
        return "[]" + qualify_type(t[2:])
    if t.startswith("*"):
        return "*" + qualify_type(t[1:])
    if t in BUILTIN_OR_QUALIFIED_TYPES or "." in t:
        return t
    return "store." + t


def parse_querier_methods() -> list[tuple[str, list[tuple[str, str]], str]]:
    src = QUERIER_GO.read_text()
    methods: list[tuple[str, list[tuple[str, str]], str]] = []
    for m in re.finditer(r"(?m)^\t([A-Z]\w+)\(([^)]*)\)\s*(.+)$", src):
        name = m.group(1)
        raw_params = m.group(2).strip()
        raw_ret = m.group(3).strip()
        params: list[tuple[str, str]] = []
        if raw_params:
            for part in raw_params.split(","):
                part = part.strip()
                if " " in part:
                    pname, ptype = part.split(None, 1)
                else:
                    pname, ptype = "", part
                params.append((pname.strip(), ptype.strip()))
        methods.append((name, params, raw_ret))
    if not methods:
        raise SystemExit(f"no methods parsed from {QUERIER_GO}")
    return methods


def render() -> tuple[str, int]:
    scopes_payload = json.loads(SCOPES_JSON.read_text())
    scopes: dict[str, str] = scopes_payload["scopes"]
    param_fields: dict[str, str] = {}
    if PARAM_FIELDS_JSON.exists():
        param_fields = json.loads(PARAM_FIELDS_JSON.read_text())
    structs = load_store_structs()
    methods = parse_querier_methods()

    missing = [name for name, _, _ in methods if name not in scopes]
    if missing:
        raise SystemExit(
            f"method_scopes.json missing {len(missing)} store.Querier method(s): {', '.join(missing)}"
        )

    needs_pgtype = False
    blocks: list[str] = []

    for name, params, raw_ret in methods:
        scope = scopes[name]
        if scope not in SCOPE_CONST:
            raise SystemExit(f"method {name} has invalid scope {scope!r}")
        scope_const = SCOPE_CONST[scope]

        # Normalize parameter names & qualify store package types.
        sig_params: list[str] = []
        call_args: list[str] = []
        tenant_checks: list[str] = []

        for idx, (pname, ptype) in enumerate(params):
            qtype = qualify_type(ptype)
            if "pgtype." in qtype:
                needs_pgtype = True
            arg_name = pname if pname and pname != "_" else f"p{idx}"
            if idx == 0 and ptype == "context.Context":
                arg_name = "ctx" if (not pname or pname == "_") else pname
            sig_params.append(f"{arg_name} {qtype}")
            call_args.append(arg_name)

            if scope == "tenant" and idx > 0:
                if pname == "tenantID" and ptype == "int64":
                    tenant_checks.append(
                        f'\tif err := CheckTenantMatch(sc, "{name}", {arg_name}); err != nil {{\n'
                    )
                elif pname == "tenantID" and ptype == "pgtype.Int8":
                    tenant_checks.append(
                        f'\tif err := CheckTenantMatchInt8(sc, "{name}", {arg_name}); err != nil {{\n'
                    )
                elif ptype in structs and "TenantID" in structs[ptype]:
                    ft = structs[ptype]["TenantID"]
                    if ft == "int64":
                        tenant_checks.append(
                            f'\tif err := CheckTenantMatch(sc, "{name}", {arg_name}.TenantID); err != nil {{\n'
                        )
                    elif ft == "pgtype.Int8":
                        tenant_checks.append(
                            f'\tif err := CheckTenantMatchInt8(sc, "{name}", {arg_name}.TenantID); err != nil {{\n'
                        )
                    else:
                        raise SystemExit(
                            f"unsupported TenantID field type {ft!r} on {ptype} for {name}"
                        )

        # Explicit tenant-field overrides for params structs whose tenant field
        # is not named TenantID (e.g. settings ScopeID).
        if scope == "tenant" and name in param_fields:
            field = param_fields[name]
            struct_type = next((pt for pn, pt in params if pn != "" and pn != "ctx" and pt in structs), "")
            if not struct_type:
                raise SystemExit(f"param_tenant_actor_fields: {name} takes no params struct")
            ft = structs[struct_type].get(field)
            if ft != "int64":
                raise SystemExit(
                    f"param_tenant_actor_fields: {name} field {field!r} must be int64, got {ft!r}"
                )
            arg_name = next(pn for pn, pt in params if pt == struct_type)
            tenant_checks.append(
                f'\tif err := CheckTenantMatch(sc, "{name}", {arg_name}.{field}); err != nil {{\n'
            )

        # Format return signature & zero return statement.
        if raw_ret == "error":
            ret_sig = "error"
            zero_return = "\t\treturn err\n"
        else:
            rm = re.match(r"^\((.+),\s*error\)$", raw_ret)
            if not rm:
                raise SystemExit(f"unsupported return type {raw_ret!r} on {name}")
            rtype = qualify_type(rm.group(1))
            if "pgtype." in rtype:
                needs_pgtype = True
            ret_sig = f"({rtype}, error)"
            zero_return = f"\t\tvar zero {rtype}\n\t\treturn zero, err\n"

        ctx_arg = call_args[0]
        lines = [
            f"func (s *ScopedQuerier) {name}({', '.join(sig_params)}) {ret_sig} {{",
        ]
        if tenant_checks:
            lines.append(f'\tsc, err := CheckMethod({ctx_arg}, "{name}", {scope_const})')
        else:
            lines.append(f'\tif _, err := CheckMethod({ctx_arg}, "{name}", {scope_const}); err != nil {{')
        if tenant_checks:
            lines.append("\tif err != nil {")
            lines.append(zero_return.rstrip("\n"))
            lines.append("\t}")
            for chk in tenant_checks:
                lines.append(chk.rstrip("\n"))
                lines.append(zero_return.rstrip("\n"))
                lines.append("\t}")
        else:
            lines.append(zero_return.rstrip("\n"))
            lines.append("\t}")
        lines.append(f"\treturn s.inner.{name}({', '.join(call_args)})")
        lines.append("}")
        blocks.append("\n".join(lines))

    imports = ['\t"context"\n']
    if needs_pgtype:
        imports.append('\t"github.com/jackc/pgx/v5/pgtype"\n')
    imports.append('\t"github.com/bushixiucai/urmail/internal/store"\n')

    header = (
        "// Code generated by scripts/sqlite/gen_isolation_decorator.py. DO NOT EDIT.\n\n"
        "package isolation\n\n"
        "import (\n"
        + "\n".join(imports)
        + ")\n\n"
    )
    return header + "\n\n".join(blocks) + "\n", len(methods)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true")
    args = ap.parse_args()

    out, count = render()
    current = OUT_GO.read_text() if OUT_GO.exists() else ""
    if args.check:
        if out != current:
            print(
                "scoped_querier_gen.go is stale: run python3 scripts/sqlite/gen_isolation_decorator.py",
                file=sys.stderr,
            )
            return 1
        print(f"scoped_querier_gen.go up to date ({count} methods)")
        return 0

    OUT_GO.write_text(out)
    print(f"wrote scoped_querier_gen.go ({count} methods)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
