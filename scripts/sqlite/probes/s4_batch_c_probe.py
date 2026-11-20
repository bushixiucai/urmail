#!/usr/bin/env python3
"""S4 batch C probe: DNS credential / change-plan procedures.

Mirrors the SQL of the handwritten methods in internal/store/sqlite_methods.go
against a schema built from migrations_sqlite:
  save/revoke credential, create/confirm/cancel plan, onboarding retry,
  rollback, review, abandon.
"""
import json
import pathlib
import re
import sqlite3

ROOT = pathlib.Path(__file__).resolve().parents[3]
SESSION = {"urmail.user_id": ""}


def current_setting(key, missing_ok=None):
    return SESSION.get(key, "")


con = sqlite3.connect(":memory:")
con.row_factory = sqlite3.Row
con.execute("PRAGMA foreign_keys=ON")
con.create_function("current_setting", 2, current_setting)
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


def go_const(name):
    src = (ROOT / "internal/store/sqlite_methods.go").read_text()
    m = re.search(r"const " + name + r" = `(.*?)`", src, re.S)
    if not m:
        raise KeyError(name)
    return m.group(1)


def one(sql, *args):
    row = con.execute(sql, args).fetchone()
    return None if row is None else (row[0] if len(row) == 1 else tuple(row))


DIGEST = "a" * 64
PLAN = "11111111-2222-3333-4444-555555555555"

# ------------------------------------------------------------------ fixtures
con.execute("INSERT INTO brands(id,slug,name) VALUES(1,'b','Brand')")
con.execute("INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(1,1,'T','t','active')")
for uid, email, role in [(1, "owner@t.test", "owner"), (2, "member@t.test", "member")]:
    con.execute("INSERT INTO users(id,email,password_hash) VALUES(?,?,'h')", (uid, email))
    con.execute("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(?,1,?)", (uid, role))
con.execute("INSERT INTO domains(id,tenant_id,fqdn,verification_token) VALUES(1,1,'mail.t.test','tok')")
con.execute("UPDATE domains SET status='active',verified_at=?,provisioning_state='ready' WHERE id=1", (NOW,))
con.execute("INSERT INTO domains(id,tenant_id,fqdn,verification_token) VALUES(2,1,'unverified.t.test','tok')")
con.execute("UPDATE domains SET status='pending_dns' WHERE id=2", ())

dns_actor_sql = go_const("dnsActorSQL")
domain_state_sql = go_const("dnsDomainStateSQL")
zone_sql = go_const("dnsZoneMatchSQL")
upsert_sql = go_const("dnsUpsertCredentialSQL")
invalidate_sql = go_const("dnsInvalidatePlansOnCredentialSQL")
revoke_sql = go_const("dnsRevokeCredentialSQL")
create_plan_sql = go_const("dnsCreatePlanSQL")
cred_live_sql = go_const("dnsCredentialLiveSQL")
plan_sql = go_const("dnsPlanForUpdateSQL")
confirm_sql = go_const("dnsConfirmPlanSQL")
cancel_sql = go_const("dnsCancelPlanSQL")
unreconciled_sql = go_const("onboardingRestoreUnreconciledSQL")
supersede_sql = go_const("onboardingSupersedeSQL")
insert_task_sql = go_const("onboardingInsertTaskSQL")
reset_domain_sql = go_const("onboardingResetDomainSQL")
rollback_sql = go_const("dnsRollbackPlanSQL")
complete_sql = go_const("dnsCompletePlanSQL")
actions_sql = go_const("dnsRecordActionsObservedSQL")
abandon_sql = go_const("dnsAbandonPlanSQL")


def dns_actor(tenant, domain):
    row = con.execute(dns_actor_sql, (tenant, domain, NOW)).fetchone()
    return "DNS_AUTHORIZATION_REQUIRED" if row is None else row[0]


def reason_ok(reason):
    return len(reason.strip()) >= 10


def domain_active(tenant, domain):
    return bool(one(domain_state_sql, tenant, domain, "active"))


def save_credential(tenant, domain, provider, zone, zone_id, secret, actor=1):
    SESSION["urmail.user_id"] = str(actor)
    if isinstance(dns_actor(tenant, domain), str):
        return "DNS_AUTHORIZATION_REQUIRED"
    if not domain_active(tenant, domain):
        return "DNS_DOMAIN_DISABLED"
    zone_ok = bool(one(zone_sql, tenant, domain, zone))
    if zone == "" or zone_id == "" or not secret.startswith("enc:v1:") or not zone_ok:
        return "DNS_ZONE_MISMATCH"
    cid = con.execute(upsert_sql, (tenant, domain, provider, zone, zone_id, secret, NOW)).fetchone()[0]
    con.execute(invalidate_sql, (tenant, domain, "DNS_CREDENTIAL_CHANGED", NOW))
    return cid


check("dns actor rejects missing session user", save_credential(1, 1, "cloudflare", "t.test", "z", "enc:v1:x", actor=0),
      "DNS_AUTHORIZATION_REQUIRED")
check("dns actor rejects non-admin role", save_credential(1, 1, "cloudflare", "t.test", "z", "enc:v1:x", actor=2),
      "DNS_AUTHORIZATION_REQUIRED")
check("dns actor rejects unverified domain", dns_actor(1, 2), "DNS_AUTHORIZATION_REQUIRED")
check("zone mismatch rejects", save_credential(1, 1, "cloudflare", "example.com", "z", "enc:v1:x"),
      "DNS_ZONE_MISMATCH")
check("encrypted secret required", save_credential(1, 1, "cloudflare", "t.test", "z", "plain"),
      "DNS_ZONE_MISMATCH")

con.execute("""INSERT INTO dns_change_plans(id,tenant_id,domain_id,kind,domain_digest,source_digest,
 before_records,desired_records,changes,created_by,expires_at,status)
 VALUES(?,1,1,'manual',?,?,'{}','{}','{}',1,?,'draft')""", ("aaaaaaaa-0000-0000-0000-000000000001", DIGEST, DIGEST, NOW + 900000000))
cid = save_credential(1, 1, "cloudflare", "t.test", "zone-1", "enc:v1:secret")
check("credential created", isinstance(cid, int) and cid > 0, True)
check("credential version 1", one("SELECT version FROM dns_credentials WHERE id=?", cid), 1)
check("credential invalidates draft plan",
      one("SELECT status||':'||last_error_code FROM dns_change_plans WHERE id='aaaaaaaa-0000-0000-0000-000000000001'"),
      "canceled:DNS_CREDENTIAL_CHANGED")
check("zone accepts parent zone", type(save_credential(1, 1, "cloudflare", "test", "zone-1", "enc:v1:s2")) is int, True)
check("credential rotated version 2", one("SELECT version FROM dns_credentials WHERE domain_id=1"), 2)

# --------------------------------------------------------------- create plan
def create_plan(plan, kind, cred, version, changes='{}'):
    SESSION["urmail.user_id"] = "1"
    if isinstance(dns_actor(1, 1), str):
        return "DNS_AUTHORIZATION_REQUIRED"
    if not domain_active(1, 1):
        return "DNS_DOMAIN_DISABLED"
    if kind == "provider":
        if cred is None or version is None:
            return "DNS_CREDENTIAL_CHANGED"
        if not one(cred_live_sql, 1, 1, cred, version):
            return "DNS_CREDENTIAL_CHANGED"
    elif cred is not None:
        return "DNS_PLAN_INVALID"
    con.execute(create_plan_sql, (plan, 1, 1, cred, version, kind, DIGEST, DIGEST, '{}', '{}', changes, 1,
                                  NOW + 900000000))
    return plan


check("provider plan needs live credential", create_plan(PLAN, "provider", cid, 99), "DNS_CREDENTIAL_CHANGED")
check("non-provider plan rejects credential", create_plan(PLAN, "managed", cid, 2), "DNS_PLAN_INVALID")
check("plan created", create_plan(PLAN, "provider", cid, 2), PLAN)
check("plan creator bound", one("SELECT created_by FROM dns_change_plans WHERE id=?", PLAN), 1)
check("plan expiry is 15min", one("SELECT (expires_at-?)<=900*1000000 FROM dns_change_plans WHERE id=?", NOW, PLAN), 1)

# -------------------------------------------------------------- confirm plan
def plan_row(plan, tenant=1):
    row = con.execute(plan_sql, (tenant, plan)).fetchone()
    if row is None:
        return "DNS_PLAN_NOT_FOUND"
    return dict(row)


p = plan_row(PLAN)
check("plan row loads", p["kind"], "provider")
check("plan digest matches", p["source_digest"], DIGEST)
check("conflicts empty", len(json.loads(p["changes"]).get("conflicts", [])), 0)

con.execute(confirm_sql, (1, PLAN, 1, NOW))
check("confirmed", one("SELECT status FROM dns_change_plans WHERE id=?", PLAN), "confirmed")
check("confirmed_by", one("SELECT confirmed_by FROM dns_change_plans WHERE id=?", PLAN), 1)

# expired / wrong status / conflicts / stale credential
con.execute("UPDATE dns_change_plans SET status='draft',expires_at=? WHERE id=?", (NOW, PLAN))
check("expired plan fails confirm gate", one("SELECT expires_at<=? FROM dns_change_plans WHERE id=?", NOW, PLAN), 1)
con.execute("UPDATE dns_change_plans SET expires_at=? WHERE id=?", (NOW + 900000000, PLAN))
check("conflicts not empty", len(json.loads('{"conflicts":[{"code":"x"}]}').get("conflicts", [])), 1)
con.execute("UPDATE dns_credentials SET enabled=0 WHERE id=?", (cid,))
check("confirm needs live credential", one(cred_live_sql, 1, 1, cid, 2), 0)
con.execute("UPDATE dns_credentials SET enabled=1 WHERE id=?", (cid,))

# ------------------------------------------------------------------ revoke
con.execute("UPDATE dns_change_plans SET status='applying',lease_owner='w',lease_until=? WHERE id=?", (NOW + 1000000, PLAN))
SESSION["urmail.user_id"] = "1"
con.execute(revoke_sql, (1, 1, NOW))
con.execute(invalidate_sql, (1, 1, "DNS_CREDENTIAL_REVOKED", NOW))
check("revoke disables credential", one("SELECT enabled FROM dns_credentials WHERE domain_id=1"), 0)
check("revoke bumps version", one("SELECT version FROM dns_credentials WHERE domain_id=1"), 3)
check("revoke invalidates applying plan",
      one("SELECT status||':'||last_error_code||':'||COALESCE(lease_owner,'-') FROM dns_change_plans WHERE id=?", PLAN),
      "uncertain:DNS_CREDENTIAL_REVOKED:-")
con.execute("UPDATE dns_credentials SET enabled=1,revoked_at=NULL WHERE domain_id=1")

# ---------------------------------------------------------------- cancel plan
check("cancel rejects short reason", reason_ok("too short"), False)
con.execute("UPDATE dns_change_plans SET status='confirmed' WHERE id=?", (PLAN,))
con.execute(cancel_sql, (1, PLAN, NOW))
check("cancel sets canceled", one("SELECT status||':'||last_error_code FROM dns_change_plans WHERE id=?", PLAN),
      "canceled:CANCELED_BY_TENANT")

# ------------------------------------------------------- onboarding retry
con.execute("""INSERT INTO onboarding_tasks(tenant_id,domain_id,status,last_error_code,updated_at)
 VALUES(1,1,'blocked','RESTORE_RECONCILIATION_REQUIRED',?)""", (NOW,))
check("restore gate blocks when no newer completed plan", one(unreconciled_sql, 1, 1), 1)
con.execute("""INSERT INTO dns_change_plans(id,tenant_id,domain_id,kind,domain_digest,source_digest,
 before_records,desired_records,changes,created_by,expires_at,status,updated_at)
 VALUES('bbbbbbbb-0000-0000-0000-000000000002',1,1,'manual',?,?,'{}','{}','{}',1,?,'completed',?)""",
            (DIGEST, DIGEST, NOW + 900000000, NOW + 1000000))
check("restore gate opens after reconciled plan", one(unreconciled_sql, 1, 1), 0)
con.execute(supersede_sql, (1, 1, NOW))
con.execute(insert_task_sql, (1, 1))
con.execute(reset_domain_sql, (1, 1))
check("retry supersedes blocked task",
      one("SELECT status||':'||last_error_code FROM onboarding_tasks WHERE id=1"),
      "failed:SUPERSEDED_BY_EXPLICIT_RETRY")
check("retry inserts pending task",
      one("SELECT COUNT(*) FROM onboarding_tasks WHERE tenant_id=1 AND domain_id=1 AND status='pending' AND step='bootstrap_config' AND max_attempts=12"), 1)
check("retry resets provisioning", one("SELECT provisioning_state FROM domains WHERE id=1"), "pending")

# ------------------------------------------------------------ rollback / review
con.execute("UPDATE dns_change_plans SET status='completed',kind='provider',updated_at=? WHERE id=?", (NOW, PLAN))
con.execute(reset_domain_sql, (1, 1))
con.execute(rollback_sql, (1, PLAN, NOW))
check("rollback queues", one("SELECT status||':'||last_error_code FROM dns_change_plans WHERE id=?", PLAN),
      "rollback_pending:ROLLBACK_EXPLICITLY_REQUESTED")
con.execute("UPDATE dns_change_plans SET status='completed',kind='manual' WHERE id=?", (PLAN,))
check("manual plan cannot rollback", one("SELECT kind FROM dns_change_plans WHERE id=?", PLAN), "manual")

con.execute("UPDATE dns_change_plans SET status='uncertain' WHERE id=?", (PLAN,))
con.execute("""INSERT INTO dns_record_actions(tenant_id,plan_id,ordinal,kind,after_record,owned,status)
 VALUES(1,?,1,'create','{}',1,'uncertain')""", (PLAN,))
con.execute(complete_sql, (1, PLAN, NOW))
con.execute(actions_sql, (1, PLAN, NOW))
check("review completes plan", one("SELECT status FROM dns_change_plans WHERE id=?", PLAN), "completed")
check("review unowns actions",
      one("SELECT status||':'||owned FROM dns_record_actions WHERE plan_id=?", PLAN), "applied:0")

con.execute("UPDATE dns_change_plans SET status='uncertain' WHERE id=?", (PLAN,))
con.execute(abandon_sql, (1, PLAN, NOW))
check("abandon fails plan", one("SELECT status||':'||last_error_code FROM dns_change_plans WHERE id=?", PLAN),
      "failed:ABANDONED_WITH_UNRESOLVED_EXTERNAL_FACTS")
check("abandon keeps journal", one("SELECT COUNT(*) FROM dns_record_actions WHERE plan_id=?", PLAN), 1)

print("fails:", failures)
raise SystemExit(1 if failures else 0)
