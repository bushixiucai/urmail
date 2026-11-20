#!/usr/bin/env python3
"""B2：UPDATE 别名机械去除（逐条登记前后对照）。

两类：
  A. `UPDATE 表 别名 SET ...`（无 FROM）：去别名，别名前缀清除；
  B. `UPDATE 表 别名 SET ... FROM 其他表 别名2 WHERE ...`：驱动表谓词保留、
     外表谓词收进 EXISTS(SELECT 1 FROM 其他表 别名2 WHERE ...)，行数语义不变。
每条登记 file/method/table/before/after 到 scripts/sqlite/b2-alias-rewrites.json。
"""
import glob
import json
import re

TARGET_TOKEN = re.compile(r"(?m)^(-- name:\s*\w+.*?)(?=^-- name:|\Z)", re.S | re.M)
UPD = re.compile(r"\bUPDATE\s+([a-z_0-9]+)(?:\s+AS\s+)?\s+([a-z_][a-z_0-9]*)\s+SET\b", re.I)


def process_block(block):
    m = UPD.search(block)
    if not m:
        return None
    table, alias = m.group(1), m.group(2)
    new = re.sub(r"(?<![\w.])" + alias + r"\.", "", block)
    new = re.sub(r"\bUPDATE\s+" + table + r"(?:\s+AS\s+)?\s+" + alias + r"\s+SET\b",
                 "UPDATE " + table + " SET", new, flags=re.I)
    new = re.sub(r"RETURNING\s+" + alias + r"\.\*", "RETURNING *", new, flags=re.I)

    frm = re.search(r"\bFROM\s+([a-z_0-9]+)\s+([a-z_][a-z_0-9]*)\s+WHERE\b", new, re.I)
    if frm:
        t2, a2 = frm.group(1), frm.group(2)
        where = new[frm.end():]
        terms = [x.strip() for x in re.split(r"\bAND\b", where.rstrip(), flags=re.I)]
        trailing = bool(terms) and terms[-1].endswith(";")
        if trailing and terms:
            terms[-1] = terms[-1].rstrip(";").strip()
        tterms, oterms = [], []
        for t in terms:
            if a2 + "." in t or t2 + "." in t:
                oterms.append(t)
            else:
                tterms.append(t)
        conj = tterms[:]
        if oterms:
            conj.append("EXISTS(SELECT 1 FROM " + t2 + " " + a2 + " WHERE "
                        + " AND ".join(oterms) + ")")
        tail = " AND ".join(conj) + (";" if trailing else "")
        new = new[:frm.start()] + " WHERE " + tail

    return {"table": table, "alias": alias,
            "before": block.strip()[:400], "after": new.strip()[:400],
            "after_full": new}


def main():
    changes = []
    for path in sorted(glob.glob("internal/store/queries/*.sql")):
        src = open(path).read()
        out = []
        pos = 0
        for m in TARGET_TOKEN.finditer(src):
            block = m.group(1)
            res = process_block(block)
            if res is None:
                continue
            head = re.search(r"(?m)^-- name:.*$", block).group(0)
            changes.append({"file": path.split("/")[-1],
                            "method": re.search(r"-- name:\s*(\w+)", head).group(1),
                            "table": res["table"], "alias": res["alias"],
                            "before": res["before"], "after": res["after"]})
            out.append(src[pos:m.start(1)])
            out.append(res["after_full"])
            pos = m.end(1)
        out.append(src[pos:])
        open(path, "w").write("".join(out))
    dump_registry(changes, "scripts/sqlite/b2-alias-rewrites.json")


def load_existing_registry(path):
    try:
        with open(path) as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def dump_registry(changes, path):
    """登记落地：空结果不覆盖已有非空登记（防重跑丢证据），改写 .new.json。"""
    if not changes:
        existing = load_existing_registry(path)
        if existing and existing.get("changes"):
            alt = path.replace(".json", ".new.json")
            json.dump({"count": len(changes), "changes": changes},
                      open(alt, "w"), indent=1, ensure_ascii=False)
            print(json.dumps({"rewritten": 0, "registry_preserved": path,
                              "empty_dump": alt}, ensure_ascii=False))
            return
    json.dump({"count": len(changes), "changes": changes},
              open(path, "w"), indent=1, ensure_ascii=False)
    print(json.dumps({"rewritten": len(changes)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
