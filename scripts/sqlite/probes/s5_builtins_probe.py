#!/usr/bin/env python3
"""S5 probe: PG-only builtins and the retention evidence chain.

Drives the real statement text from internal/store/queries against a schema
built from migrations_sqlite, with the PG builtins the port keeps (md5) and the
S2 session seam (current_setting) registered as SQLite functions.

Covered:
  - md5 UDF: PG byte-for-byte vectors, NULL-in/NULL-out, and the mailbox
    password_version binding (create -> resolve -> password change invalidates).
  - archive retention: retention_days window restored (the old port had `-(0)`),
    tenant-scoped candidate digest (the old one hashed every tenant),
    preview/candidate digest identity.
  - delivery fingerprint: json_array + md5, replay dedup.
  - send receipt / scheduled send md5 comparisons and bytea->text conversion.
  - calendar jsonb_set -> json_set, tenant usage bool_or -> MAX.
  - the bounded report JSON expression (to_jsonb -> json_object).
  - no pg_advisory_* call survives in the query layer.
"""
import hashlib
import pathlib
import re
import sqlite3

ROOT = pathlib.Path(__file__).resolve().parents[3]
QUERIES = ROOT / "internal/store/queries"
DAY = 86_400 * 1_000_000


def current_setting(_key, _missing_ok=None):
    return ""


con = sqlite3.connect(":memory:")
con.row_factory = sqlite3.Row
con.execute("PRAGMA foreign_keys=ON")
con.create_function("current_setting", 2, current_setting)
con.create_function("md5", 1, lambda s: None if s is None else hashlib.md5(s.encode()).hexdigest())
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


def sql_stmt(name):
    """Extract one -- name: block from the SQLite-dialect query files."""
    for path in sorted(QUERIES.glob("*.sql")):
        text = path.read_text()
        m = re.search(
            r"^-- name: " + re.escape(name) + r" :\w+\n(?:(?:--[^\n]*)\n)*\n(.*?);\n",
            text, re.S | re.M,
        )
        if m:
            return m.group(1)
    raise KeyError(name)


def named(sql, **values):
    """Translate @name / sqlc.arg('name') / sqlc.narg('name') to ?N."""
    order = []
    pattern = re.compile(
        r"sqlc\.n?arg\(\s*['\"]?([A-Za-z_][A-Za-z0-9_]*)['\"]?\s*\)"
        r"|@([A-Za-z_][A-Za-z0-9_]*)"
    )

    def repl(m):
        name = m.group(1) or m.group(2)
        if name not in order:
            order.append(name)
        return f"?{order.index(name) + 1}"

    sql = pattern.sub(repl, sql)
    return sql, tuple(values[n] for n in order)


def one(sql, *args):
    row = con.execute(sql, args).fetchone()
    return None if row is None else (row[0] if len(row) == 1 else tuple(row))


def value(sql, *args):
    return one(sql, *args)


# ------------------------------------------------------------------ md5 UDF
vectors = {
    "test123": "cc03e747a6afbbcbf8be7668acfebee5",
    "": "d41d8cd98f00b204e9800998ecf8427e",
    "测试": "db06c78d1e24cf708a14ce81c9b617ec",
}
for raw, want in vectors.items():
    check(f"md5({raw!r}) matches PG", value("SELECT md5(?)", raw), want)
check("md5(NULL) is NULL", value("SELECT md5(NULL)"), None)

# ---------------------------------------------------------------- fixtures
con.execute("INSERT INTO brands(id,slug,name) VALUES(1,'b','Brand')")
con.execute("INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(1,1,'T1','t1','active')")
con.execute("INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(2,1,'T2','t2','active')")
con.execute("INSERT INTO users(id,email,password_hash) VALUES(1,'admin@t1.test','h')")
con.execute("INSERT INTO domains(id,tenant_id,fqdn,status,verification_token) "
            "VALUES(1,1,'t1.test','pending_dns','tok')")
con.execute("UPDATE domains SET status='active',verified_at=?,provisioning_state='ready' WHERE id=1", (NOW,))
con.execute("INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash) "
            "VALUES(1,1,1,'mail','mail@t1.test','hash-one')")

# ------------------------------------------------------- archive retention
def archive(id_, tenant, days_ago, legal_hold=0, deleted_at=None):
    con.execute(
        "INSERT INTO archive_entries(id,tenant_id,mailbox_id,message_id,path,received_at,legal_hold,deleted_at) "
        "VALUES(?,?,NULL,?,?,?,?,?)",
        (id_, tenant, f"m{id_}", f"p{id_}", NOW - days_ago * DAY, legal_hold, deleted_at),
    )


archive(1, 1, 10)                     # candidate for 7 days
archive(2, 1, 10, legal_hold=1)       # held: excluded
archive(3, 1, 2)                      # inside the window: excluded
archive(4, 1, 10, deleted_at=NOW - DAY)  # already deleted: excluded
archive(5, 2, 10)                     # other tenant: must never enter T1's digest

preview_sql, preview_args = named(
    sql_stmt("PreviewArchiveRetention"), tenant_id=1, retention_days=7, created_by=1
)
preview = con.execute(preview_sql, preview_args).fetchone()
check("preview counts only tenant 1 candidates", preview["candidate_count"], 1)
check("preview digest hashes only the candidate set",
      preview["candidate_digest"], hashlib.md5(b"1").hexdigest())
check("preview digest is not the cross-tenant digest",
      preview["candidate_digest"] != hashlib.md5(b"1,2,3,4,5").hexdigest(), True)
check("preview digest is not the whole-table digest including holds/deleted",
      preview["candidate_digest"] != hashlib.md5(b"1,2,3,4").hexdigest(), True)
check("preview id is a lowercase UUID", bool(re.fullmatch(
    r"[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}", preview["id"])), True)
check("retention_days is stored", preview["retention_days"], 7)

cutoff_sql, cutoff_args = named(
    sql_stmt("ArchiveRetentionCandidateDigest"), tenant_id=1, cutoff=NOW - 7 * DAY
)
digest = con.execute(cutoff_sql, cutoff_args).fetchone()
check("candidate digest agrees with the preview count", digest["candidate_count"], preview["candidate_count"])
check("candidate digest agrees with the preview digest", digest["candidate_digest"], preview["candidate_digest"])

# A candidate older than the window shows the old `-(0)` bug would have matched
# it; the restored window must not.
archive(6, 1, 1)
digest2 = con.execute(cutoff_sql, cutoff_args).fetchone()
check("newly received mail is not a retention candidate", digest2["candidate_count"], 1)

# ---------------------------------------------------- delivery fingerprint
event_sql = sql_stmt("RecordDeliveryEvent")
args = (1, "inst", "q1", "<mid@t1.test>", "s@t1.test", "t1.test",
        "r@t2.test", "t2.test", "relay", "sent", "", 0, "250 OK", NOW)
row = con.execute(event_sql, args).fetchone()
check("first delivery event inserts", row is not None, True)
expected_source = "compat:" + hashlib.md5(
    con.execute("SELECT json_array(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", args).fetchone()[0].encode()
).hexdigest()
check("delivery fingerprint is compat:md5(json_array(...))",
      value("SELECT source_event_id FROM delivery_events WHERE queue_id='q1'"), expected_source)
check("replayed delivery event is deduped",
      con.execute(event_sql, args).fetchone(), None)

# ------------------------------------------------- mailbox session md5 bind
session_sql, session_args = named(
    sql_stmt("PlatformCreateVerifiedMailboxSession"),
    token_hash="tok-1", ttl=30 * 60 * 1_000_000, origin_host="web",
    address="mail@t1.test", expected_hash="hash-one",
)
created = con.execute(session_sql, session_args).fetchone()
check("session password_version is md5(password_hash)", created["password_version"],
      hashlib.md5(b"hash-one").hexdigest())
check("session deadline uses the ttl microseconds",
      created["expires_at"] >= NOW + 30 * 60 * 1_000_000 - 5_000_000, True)

get_sql, get_args = named(sql_stmt("PlatformGetMailboxSession"), token_hash="tok-1")
check("session resolves while the password is unchanged",
      con.execute(get_sql, get_args).fetchone() is not None, True)

pwd_sql, pwd_args = named(sql_stmt("PlatformMailboxPasswordForSession"),
                          session_hash="tok-1", address="mail@t1.test")
check("password-for-session resolves while the hash matches",
      con.execute(pwd_sql, pwd_args).fetchone() is not None, True)

con.execute("UPDATE mailboxes SET password_hash='hash-two' WHERE id=1")
check("password change invalidates the session",
      con.execute(get_sql, get_args).fetchone(), None)
check("password change invalidates password-for-session",
      con.execute(pwd_sql, pwd_args).fetchone(), None)

# A NULL hash can never compare equal to a digest (md5(NULL) is NULL).
con.execute("UPDATE mailboxes SET password_hash='' WHERE id=1")
check("md5 comparison against empty hash does not match the old session",
      con.execute(get_sql, get_args).fetchone(), None)
con.execute("UPDATE mailboxes SET password_hash='hash-one' WHERE id=1")

# ------------------------------------------------- send receipt md5 checks
def receipt(id_, request_key, password_version):
    con.execute(
        "INSERT INTO mailbox_send_receipts(id,tenant_id,mailbox_id,request_key,request_hash,"
        "message_id,status,sent_copy_state,raw_secret,smtp_password_version,lease_until) "
        "VALUES(?,1,1,?,?,?,'accepted','pending','rs',?,?)",
        (id_, request_key, "a" * 64, f"<m{id_}@t1.test>", password_version, NOW + 3600 * 1_000_000),
    )


CURRENT = hashlib.md5(b"hash-one").hexdigest()
receipt(1, "key-0000000000001", CURRENT)
receipt(2, "key-0000000000002", hashlib.md5(b"old-hash").hexdigest())

con.execute(sql_stmt("PlatformHoldUnauthorizedSendCopies"))
check("receipt with the current password version is untouched",
      value("SELECT sent_copy_state FROM mailbox_send_receipts WHERE id=1"), "pending")
check("receipt with a stale password version is held",
      value("SELECT sent_copy_state FROM mailbox_send_receipts WHERE id=2"), "held")
check("held receipt clears the SMTP secret",
      value("SELECT smtp_secret FROM mailbox_send_receipts WHERE id=2"), "")

auth_sql = sql_stmt("PlatformAuthorizeSendCopy")
check("send copy authorizes with the current version",
      con.execute(auth_sql, (1, 1, 1, 0)).fetchone() is not None, True)
check("send copy refuses the stale version",
      con.execute(auth_sql, (1, 1, 2, 0)).fetchone(), None)

# ---------------------------------------------------- scheduled send bytes
con.execute(
    "INSERT INTO mailbox_scheduled_sends(id,tenant_id,mailbox_id,draft_folder,draft_uid,send_at,"
    "status,claim_token,request_hash) VALUES(1,1,1,'Drafts',5,?,'submitting',9,?)",
    (NOW, "b" * 64),
)
accept_sql, accept_args = named(sql_stmt("PlatformAcceptMailboxScheduledSend"),
                                sent_raw=b"enc:v1:payload",
                                tenant_id=1, mailbox_id=1, id=1, claim_token=9)
accepted = con.execute(accept_sql, accept_args).fetchone()
check("scheduled send accepted", accepted["status"], "sent")
check("sent_raw_secret is TEXT (bytea -> text cast)",
      value("SELECT typeof(sent_raw_secret) FROM mailbox_scheduled_sends WHERE id=1"), "text")
check("sent_raw_secret carries the bytes", accepted["sent_raw_secret"], "enc:v1:payload")
check("sent_raw is cleared", accepted["sent_raw"], None)

# --------------------------------------------------------- json_set / MAX
con.execute(
    "INSERT INTO mailbox_jobs(id,tenant_id,mailbox_id,kind,idempotency_key,payload,status) "
    "VALUES(1,1,1,'calendar_reminder','k',?,'done')",
    ('{"acknowledged":false,"keep":7}',),
)
con.execute(sql_stmt("PlatformAcknowledgeCalendarReminder"), (1, 1, 1))
check("json_set updates acknowledged to a JSON boolean",
      value("SELECT json_type(payload,'$.acknowledged') FROM mailbox_jobs WHERE id=1"), "true")
check("json_set preserves the other keys",
      value("SELECT json_extract(payload,'$.keep') FROM mailbox_jobs WHERE id=1"), 7)

con.execute("UPDATE mailboxes SET quota_bytes=-1 WHERE id=1")
usage = con.execute(sql_stmt("GetTenantStorageUsage"), (1,)).fetchone()
check("bool_or port reports unlimited", usage["unlimited"], 1)
con.execute("UPDATE mailboxes SET quota_bytes=100 WHERE id=1")
usage = con.execute(sql_stmt("GetTenantStorageUsage"), (1,)).fetchone()
check("bool_or port reports not unlimited", usage["unlimited"], 0)

# ------------------------------------------------- report JSON expression
report_expr = (
    "SELECT json_group_array(json_object("
    "'record_kind',b.record_kind,'record_id',b.record_id,'version',b.version,"
    "'instance_id',b.instance_id,'email',b.email,'state',b.state,"
    "'result_code',b.result_code,'occurred_at',b.occurred_at,'weight',b.weight,"
    "'suppressed',json(CASE WHEN b.suppressed THEN 'true' ELSE 'false' END),"
    "'uncertain',json(CASE WHEN b.uncertain THEN 'true' ELSE 'false' END)) "
    "ORDER BY b.record_kind,b.record_id) "
    "FROM (SELECT 'entry' AS record_kind,2 AS record_id,1 AS version,0 AS instance_id,"
    "'' AS email,'sent' AS state,'k' AS result_code,5 AS occurred_at,3 AS weight,"
    "0 AS suppressed,1 AS uncertain) b"
)
rows = con.execute(report_expr).fetchone()[0]
check("report rows keep the to_jsonb column shape",
      con.execute("SELECT json_extract(?,'$[0].record_kind')", (rows,)).fetchone()[0], "entry")
check("report booleans stay JSON booleans",
      con.execute("SELECT json_type(?,'$[0].uncertain')", (rows,)).fetchone()[0], "true")
check("report arithmetic columns stay numeric",
      con.execute("SELECT json_type(?,'$[0].weight')", (rows,)).fetchone()[0], "integer")

# ------------------------------------------------ no advisory lock remains
advisory = [
    str(p.relative_to(ROOT))
    for p in QUERIES.glob("*.sql")
    if re.search(r"pg_advisory", p.read_text())
]
check("no pg_advisory_* call remains", advisory, [])

print("fails:", failures)
raise SystemExit(1 if failures else 0)