#!/usr/bin/env python3
"""S7 probe: the 8 duplicate-name decisions in
PHASE1-S2-BACKEND-REENTRY-20261120-APPENDIX.md.

Covered:
  - the 7 Go/.sql duplicate names are gone from the .sql layer (the Go
    handwritten methods are now the only implementation);
  - the archive-retention Mark* statements compose a two-column key set, so
    the row-value IN matches the SELECT arity (2 vs 3 was the needs-review
    bug), and only eligible rows are stamped/deleted;
  - no .sql statement uses a query name as a table name (the removed V2 shape).
"""
import hashlib
import pathlib
import re
import sqlite3

ROOT = pathlib.Path(__file__).resolve().parents[3]
QUERIES = ROOT / "internal/store/queries"
METHODS = ROOT / "internal/store/sqlite_methods.go"
DAY = 86_400 * 1_000_000

DUPLICATES = [
    "PlatformRevokeMailboxSessions",
    "PlatformConsumeMailboxOperationTickets",
    "PlatformApplyArchiveRetentionCandidates",
    "PlatformApplyArchiveRetentionMarkContent",
    "PlatformApplyArchiveRetentionMarkDeleted",
    "PlatformRecordMarketingFlowNodeExecution",
    "PlatformBumpMarketingFlowAttempt",
]


def go_const(name):
    src = METHODS.read_text()
    m = re.search(r"^const " + re.escape(name) + r" = (.*?)(?=\n\n)", src, re.S | re.M)
    if not m:
        raise KeyError(name)
    rhs = m.group(1)
    tokens = re.findall(r"`[^`]*`|\w+", rhs)
    out = []
    for tok in tokens:
        if tok.startswith("`"):
            out.append(tok[1:-1])
        else:
            out.append(go_const(tok))
    return "".join(out)


con = sqlite3.connect(":memory:")
con.row_factory = sqlite3.Row
con.execute("PRAGMA foreign_keys=ON")
for f in ["0001_init.sql", "0002_write_side_effects.sql"]:
    con.executescript((ROOT / "migrations_sqlite" / f).read_text())

NOW = con.execute("SELECT CAST(strftime('%s','now') AS INTEGER)*1000000").fetchone()[0]
failures = []


def check(label, got, want):
    if got == want:
        print(f"PASS {label}")
    else:
        failures.append(label)
        print(f"FAIL {label}: got {got!r} want {want!r}")


# ------------------------------------------------- duplicate name removal
all_sql = "\n".join(p.read_text() for p in sorted(QUERIES.glob("*.sql")))
for name in DUPLICATES:
    check(f"{name} has no .sql statement", re.search(r"(?m)^-- name:\s*" + re.escape(name) + r"\b", all_sql), None)

# No query name used as a table (the deleted V2 shape).
query_names = set(re.findall(r"(?m)^-- name:\s*(\w+)", all_sql))
table_use = [n for n in query_names
             if re.search(r"\b(?:FROM|JOIN)\s+" + re.escape(n) + r"\b", all_sql)]
check("no query name is used as a table", table_use, [])

# ------------------------------------------------- two-column key arity
keys = go_const("archiveRetentionCandidateKeysSQL")
check("key set projects exactly two columns",
      keys.startswith("SELECT a.id, a.tenant_id FROM archive_entries a"), True)
check("key set does not project the preview",
      re.search(r"SELECT\s+a\.id\s*,\s*a\.tenant_id\s*,\s*p\.confirmed_preview", keys), None)

broken = [str(p.relative_to(ROOT)) for p in QUERIES.glob("*.sql")
          if re.search(r"\(\s*\w+\s*,\s*\w+\s*\)\s+IN\s*\(\s*SELECT\s+a\.id\s*,\s*a\.tenant_id\s*,\s*p\.confirmed_preview",
                       p.read_text(), re.I)]
check("no .sql compares 2-column IN against 3-column SELECT", broken, [])

# ------------------------------------------------- retention chain behavior
con.execute("INSERT INTO brands(id,slug,name) VALUES(1,'b','Brand')")
con.execute("INSERT INTO users(id,email,password_hash) VALUES(1,'admin@t1.test','h')")
con.execute("INSERT INTO users(id,email,password_hash) VALUES(2,'other@t1.test','h')")
con.execute("INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(1,1,'T1','t1','active')")
con.execute("INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(2,1,'T2','t2','active')")
con.execute("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(1,1,'owner')")
con.execute(
    "INSERT INTO compliance_grants(id,tenant_id,user_id,scope,expires_at,revoked,reason,granted_by,owner_confirmed_at,created_at,updated_at) "
    "VALUES(1,1,1,'retention_manage',?,'0','retention audit reason',1,?,?,?)",
    (NOW + 30 * DAY, NOW, NOW, NOW),
)
PREVIEW = "11111111-2222-4333-8444-555555555555"
con.execute(
    "INSERT INTO retention_previews(id,tenant_id,retention_days,candidate_count,candidate_digest,created_by,created_at,expires_at,confirmed_at,authorization_grant_id) "
    "VALUES(?,1,7,1,?,1,?,?,?,1)",
    (PREVIEW, hashlib.md5(b"1").hexdigest(), NOW, NOW + 15 * 60 * 1_000_000, NOW),
)
con.execute(
    "INSERT INTO retention_policies(tenant_id,retention_days,action,active,created_by,confirmed_preview,authorization_grant_id) "
    "VALUES(1,7,'soft_delete',1,1,?,1)",
    (PREVIEW,),
)


def archive(id_, tenant, days_ago, legal_hold=0, deleted_at=None):
    con.execute(
        "INSERT INTO archive_entries(id,tenant_id,mailbox_id,message_id,path,received_at,legal_hold,deleted_at) "
        "VALUES(?,?,NULL,?,?,?,?,?)",
        (id_, tenant, f"m{id_}", f"p{id_}", NOW - days_ago * DAY, legal_hold, deleted_at),
    )
    con.execute(
        "INSERT INTO archive_content(entry_id,tenant_id,body_text,body_html) VALUES(?,?,'body','')",
        (id_, tenant),
    )


archive(1, 1, 10)                          # eligible
archive(2, 1, 10, legal_hold=1)            # hold wins
archive(3, 1, 2)                           # inside the window
archive(4, 2, 10)                          # other tenant (no policy)
archive(5, 1, 10, deleted_at=NOW - DAY)    # already deleted

mark_content = go_const("archiveRetentionMarkContentSQL")
mark_deleted = go_const("archiveRetentionMarkDeletedSQL")

con.execute(mark_content)
check("MarkContent stamps the preview on the eligible row",
      con.execute("SELECT retention_preview FROM archive_content WHERE entry_id=1").fetchone()[0], PREVIEW)
for entry in (2, 3, 4, 5):
    check(f"MarkContent leaves entry {entry} unstamped",
          con.execute("SELECT retention_preview FROM archive_content WHERE entry_id=?", (entry,)).fetchone()[0], None)

con.execute(mark_deleted)
check("MarkDeleted soft-deletes the eligible row",
      con.execute("SELECT deleted_at FROM archive_entries WHERE id=1").fetchone()[0] is not None, True)
for entry in (2, 3, 4):
    check(f"MarkDeleted leaves entry {entry} alone",
          con.execute("SELECT deleted_at FROM archive_entries WHERE id=?", (entry,)).fetchone()[0], None)
check("MarkDeleted keeps the earlier deleted_at on entry 5",
      con.execute("SELECT deleted_at FROM archive_entries WHERE id=5").fetchone()[0], NOW - DAY)

print("fails:", failures)
raise SystemExit(1 if failures else 0)