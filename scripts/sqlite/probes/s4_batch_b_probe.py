#!/usr/bin/env python3
"""S4 batch B probe: archive case create, archive soft delete, panel credential
rotation / MFA change (multi-statement Go procedures in
internal/store/sqlite_methods.go).

Runs the handwritten layer's SQL against a schema built from migrations_sqlite
and asserts the PG-function semantics survive:

  * urmail_create_archive_case  -> grant/expiry/request_key idempotency
  * urmail_soft_delete_archive  -> legal hold / purge / role gates
  * urmail_rotate_panel_password / urmail_change_panel_mfa -> credential
    generation bump (panel_credential_version trigger), session revocation,
    audit row, rehash escape hatch
"""
import pathlib
import re
import sqlite3

ROOT = pathlib.Path(__file__).resolve().parents[3]

SESSION = {}


def current_setting(key, missing_ok=None):
    return SESSION.get(key, "")


con = sqlite3.connect(":memory:")
con.row_factory = sqlite3.Row
con.execute("PRAGMA foreign_keys=ON")
con.create_function("current_setting", 2, current_setting)
for f in ["0001_init.sql", "0002_write_side_effects.sql"]:
    con.executescript((ROOT / "migrations_sqlite" / f).read_text())

# S6 removed the append-only INSERT trigger for the platform writer path, so
# the credential procedures can write their audit row. IF EXISTS keeps the
# probe runnable against a pre-S6 schema too.
con.execute("DROP TRIGGER IF EXISTS audit_logs_append_only_insert")

NOW = con.execute("SELECT CAST(strftime('%s','now') AS INTEGER)*1000000").fetchone()[0]
DAY = 86400000000

fails = []


def check(label, got, want):
    if got == want:
        print(f"PASS {label}")
    else:
        fails.append(label)
        print(f"FAIL {label}: got {got!r} want {want!r}")


def go_const(name):
    src = (ROOT / "internal/store/sqlite_methods.go").read_text()
    m = re.search(r"const " + name + r" = `(.*?)`", src, re.S)
    if not m:
        raise KeyError(name)
    return m.group(1)


def one(sql, *args):
    row = con.execute(sql, args).fetchone()
    return None if row is None else tuple(row) if len(row) > 1 else row[0]


def affected(sql, *args):
    return con.execute(sql, args).rowcount


# ------------------------------------------------------------------ fixtures
con.execute("INSERT INTO brands(id,slug,name) VALUES(1,'b','Brand')")
con.execute("INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(1,1,'T','t','active')")
for uid, email in [(1, "owner@t.test"), (2, "admin@t.test")]:
    con.execute("INSERT INTO users(id,email,password_hash) VALUES(?,?,'hash0')", (uid, email))
con.execute("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(1,1,'owner')")
con.execute("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(2,1,'admin')")
con.execute("""INSERT INTO compliance_grants(id,tenant_id,user_id,scope,expires_at,revoked,reason,granted_by,owner_confirmed_at)
 VALUES(10,1,1,'full_content',?,0,'case work',2,1)""", (NOW + 30 * DAY,))

CREATE_KEY = "11111111-2222-3333-4444-555555555555"
grant_sql = go_const("archiveCaseGrantSQL")
existing_sql = go_const("archiveCaseExistingSQL")
insert_sql = go_const("archiveCaseInsertSQL")
member_sql = go_const("archiveCaseAddMemberSQL")


def create_case(title, purpose, expiry, key, actor=1, tenant=1):
    """Mirror of Queries.ArchiveCasesCreate (SQL parts only)."""
    if not re.fullmatch(r"[0-9a-f-]{36}", key):
        return "CASE_NOT_AUTHORIZED"
    if expiry <= NOW or expiry > NOW + 366 * DAY:
        return "CASE_NOT_AUTHORIZED"
    if not one(grant_sql, tenant, actor, "full_content", NOW):
        return "CASE_NOT_AUTHORIZED"
    row = con.execute(existing_sql, (tenant, actor, key)).fetchone()
    if row is not None:
        if row["title"] != title or row["purpose"] != purpose or row["expires_at"] != expiry:
            return "CASE_REQUEST_KEY_MISMATCH"
        return row["id"]
    cur = con.execute(insert_sql, (tenant, title, purpose, actor, expiry, key))
    cid = cur.fetchone()[0]
    con.execute(member_sql, (tenant, cid, actor))
    return cid


check("case rejects malformed request key", create_case("t", "p", NOW + 30 * DAY, "not-a-uuid"),
      "CASE_NOT_AUTHORIZED")
check("case rejects missing grant", create_case("t", "p", NOW + 30 * DAY, CREATE_KEY, actor=2),
      "CASE_NOT_AUTHORIZED")
check("case rejects past expiry", create_case("t", "p", NOW - DAY, CREATE_KEY), "CASE_NOT_AUTHORIZED")
check("case rejects >1y expiry", create_case("t", "p", NOW + 370 * DAY, CREATE_KEY), "CASE_NOT_AUTHORIZED")
first = create_case("Case one", "purpose", NOW + 30 * DAY, CREATE_KEY)
check("case created", isinstance(first, int) and first > 0, True)
check("case creator is member", one(
    "SELECT COUNT(*) FROM archive_investigation_members WHERE tenant_id=1 AND investigation_id=?", first), 1)
check("case replay same payload returns id", create_case("Case one", "purpose", NOW + 30 * DAY, CREATE_KEY), first)
check("case replay changed title is stale", create_case("Case two", "purpose", NOW + 30 * DAY, CREATE_KEY),
      "CASE_REQUEST_KEY_MISMATCH")
check("case replay changed expiry is stale", create_case("Case one", "purpose", NOW + 31 * DAY, CREATE_KEY),
      "CASE_REQUEST_KEY_MISMATCH")
second = create_case("Case one", "purpose", NOW + 30 * DAY, "99999999-2222-3333-4444-555555555555")
check("case new request key creates new case", isinstance(second, int) and second != first, True)

# ---------------------------------------------------------- archive soft delete
con.execute("""INSERT INTO domains(id,tenant_id,fqdn,verification_token)
 VALUES(1,1,'t.test','tok')""")
con.execute("UPDATE domains SET status='active', verified_at=?, provisioning_state='ready' WHERE id=1", (NOW,))
con.execute("INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash) VALUES(1,1,1,'u','u@t.test','x')")
con.execute("""INSERT INTO archive_entries(id,tenant_id,mailbox_id,message_id,path,received_at,size_bytes,raw_sha256)
 VALUES(1,1,1,'m1','/a/m1',?,10,'sha'),(2,1,1,'m2','/a/m2',?,10,'sha'),(3,1,1,'m3','/a/m3',?,10,'sha')""",
            (NOW, NOW, NOW))
con.execute("""INSERT INTO retention_previews(id,tenant_id,retention_days,candidate_count,candidate_digest,created_by,expires_at)
 VALUES('preview-1',1,30,1,'d',1,?)""", (NOW + 30 * DAY,))
con.execute("INSERT INTO archive_content(entry_id,tenant_id,retention_preview) VALUES(1,1,'preview-1')")
con.execute("""INSERT INTO retention_previews(id,tenant_id,retention_days,candidate_count,candidate_digest,created_by,expires_at)
 VALUES('preview-2',1,30,1,'d',1,?)""", (NOW + 30 * DAY,))
con.execute("INSERT INTO archive_content(entry_id,tenant_id,retention_preview,purge_started_at) VALUES(2,1,'preview-2',?)", (NOW,))
con.execute("""INSERT INTO retention_previews(id,tenant_id,retention_days,candidate_count,candidate_digest,created_by,expires_at)
 VALUES('preview-3',1,30,1,'d',1,?)""", (NOW + 30 * DAY,))
con.execute("INSERT INTO archive_content(entry_id,tenant_id,retention_preview) VALUES(3,1,'preview-3')")
con.execute("UPDATE archive_entries SET legal_hold=1 WHERE id=3")

select_sql = go_const("softDeleteArchiveSelectSQL")
mark_sql = go_const("softDeleteArchiveMarkSQL")
preview_sql = go_const("softDeleteArchivePreviewSQL")


def soft_delete(entry, tenant=1):
    """Mirror of Queries.SoftDeleteArchiveEntry (row selection + writes)."""
    row = con.execute(select_sql, (tenant, entry)).fetchone()
    if row is None:
        return "NO_ROWS"
    con.execute(mark_sql, (tenant, entry, NOW))
    con.execute(preview_sql, (tenant, entry))
    return row["id"]


check("soft delete legal hold is no-rows", soft_delete(3), "NO_ROWS")
check("soft delete mid-purge is no-rows", soft_delete(2), "NO_ROWS")
check("soft delete deletes", soft_delete(1), 1)
check("soft delete sets deleted_at", one("SELECT deleted_at FROM archive_entries WHERE id=1"), NOW)
check("soft delete clears retention preview", one("SELECT retention_preview FROM archive_content WHERE entry_id=1"), None)
check("soft delete repeat is no-rows", soft_delete(1), "NO_ROWS")
check("soft delete legal hold untouched", one("SELECT deleted_at FROM archive_entries WHERE id=3"), None)

# ------------------------------------------------------- credential rotation
con.execute("""INSERT INTO sessions(id,user_id,tenant_id,csrf_token,expires_at,kind,credential_version)
 VALUES('keep',1,1,'c',?,'panel',0),('other',1,1,'c',?,'panel',0),('expired',1,1,'c',?,'panel',0)""",
            (NOW + DAY, NOW + DAY, NOW - DAY))

user_pw_sql = go_const("panelUserPasswordSQL")
session_sql = go_const("panelSessionSQL")
rotate_sql = go_const("panelRotatePasswordSQL")
drop_sql = go_const("panelDropOtherSessionsSQL")
touch_sql = go_const("panelTouchKeptSessionSQL")
audit_sql = go_const("panelCredentialAuditSQL")
gen_sql = go_const("panelCredentialVersionSQL")


def rotate(user, expected_hash, expected_version, new_hash, keep, now=NOW):
    """Mirror of Queries.PlatformRotatePanelPassword."""
    if con.execute(user_pw_sql, (user, expected_hash, expected_version)).fetchone() is None:
        return "CREDENTIAL_CHANGED"
    if con.execute(session_sql, (keep, user, expected_version, now)).fetchone() is None:
        return "SESSION_REVOKED"
    con.execute(rotate_sql, (user, new_hash, now))
    generation = con.execute(gen_sql, (user,)).fetchone()[0]
    con.execute(drop_sql, (user, keep))
    con.execute(touch_sql, (user, keep, generation))
    con.execute(audit_sql, (user, str(user), '{"credential_version":%d}' % generation))
    return generation


check("rotate rejects changed hash", rotate(1, "stale", 0, "hash1", "keep"), "CREDENTIAL_CHANGED")
check("rotate rejects revoked session", rotate(1, "hash0", 0, "hash1", "expired"), "SESSION_REVOKED")
check("rotate bumps generation", rotate(1, "hash0", 0, "hash1", "keep"), 1)
check("rotate keeps only keep session", sorted(r["id"] for r in con.execute("SELECT id FROM sessions WHERE user_id=1")),
      ["keep"])
check("rotate updates kept session version", one("SELECT credential_version FROM sessions WHERE id='keep'"), 1)
check("rotate sets password_changed_at", one("SELECT password_changed_at FROM users WHERE id=1"), NOW)
check("rotate writes audit", one("SELECT detail FROM audit_logs WHERE actor_user_id=1 AND action='auth.credential.rotate'"),
      '{"credential_version":1}')
check("rotate second pass sees new version", rotate(1, "hash1", 1, "hash2", "keep"), 2)

SESSION["urmail.password_rehash"] = "true"
con.execute("UPDATE users SET password_hash='rehashed' WHERE id=1")
check("rehash does not bump generation", one("SELECT credential_version FROM users WHERE id=1"), 2)
SESSION.pop("urmail.password_rehash")
con.execute("UPDATE users SET password_hash='hash2' WHERE id=1")
check("plain password update bumps generation", one("SELECT credential_version FROM users WHERE id=1"), 3)

# ------------------------------------------------------------- MFA change
con.execute("INSERT INTO sessions(id,user_id,tenant_id,csrf_token,expires_at,kind,credential_version) VALUES('mkeep',1,1,'c',?,'panel',3)",
            (NOW + DAY,))
user_cred_sql = go_const("panelUserCredentialSQL")
set_mfa_sql = go_const("panelSetMFASQL")
touch_mfa_sql = go_const("panelTouchKeptMFASessionSQL")


def change_mfa(user, expected_version, secret, enabled, keep, now=NOW):
    """Mirror of Queries.PlatformChangePanelMFA."""
    if con.execute(user_cred_sql, (user, expected_version)).fetchone() is None:
        return "CREDENTIAL_CHANGED"
    if con.execute(session_sql, (keep, user, expected_version, now)).fetchone() is None:
        return "SESSION_REVOKED"
    con.execute(set_mfa_sql, (user, secret, 1 if enabled else 0, "[]"))
    generation = con.execute(gen_sql, (user,)).fetchone()[0]
    con.execute(drop_sql, (user, keep))
    con.execute(touch_mfa_sql, (user, keep, generation, now if enabled else None))
    con.execute(audit_sql, (user, str(user), '{"credential_version":%d}' % generation))
    return generation


check("mfa rejects stale version", change_mfa(1, 2, "seed", True, "mkeep"), "CREDENTIAL_CHANGED")
check("mfa enable bumps generation", change_mfa(1, 3, "enc:v1:seed", True, "mkeep"), 4)
check("mfa enable sets flag", one("SELECT mfa_enabled FROM users WHERE id=1"), 1)
check("mfa enable marks kept session verified", one("SELECT mfa_verified_at FROM sessions WHERE id='mkeep'"), NOW)
check("mfa disable bumps generation", change_mfa(1, 4, None, False, "mkeep"), 5)
check("mfa disable clears verified at", one("SELECT mfa_verified_at FROM sessions WHERE id='mkeep'"), None)
check("mfa disable nulls secret", one("SELECT mfa_secret FROM users WHERE id=1"), None)
check("mfa audit rows", one("SELECT COUNT(*) FROM audit_logs WHERE actor_user_id=1 AND action='auth.credential.rotate'"), 4)

print("fails:", fails)
raise SystemExit(1 if fails else 0)
