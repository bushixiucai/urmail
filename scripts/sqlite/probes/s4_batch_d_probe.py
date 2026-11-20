#!/usr/bin/env python3
"""S4 batch D probe: marketing dispatch + flow procedures.

Drives the SQL constants of internal/store/sqlite_methods.go against a schema
built from migrations_sqlite, with the orchestration mirrored in Python (same
approach as the batch B/C probes: the statement text under test is the Go
const, not a copy).

Covered: quota window (including the -infinity → int64-min default fix), rate
limiting, local-midnight boundary and timezone change, recipient state machine
(stale/paused/suppressed/authorized/finished), bounce suppression, feedback
idempotency, flow begin/commit/release/retry.
"""
import pathlib
import re
import sqlite3
import time
from datetime import datetime

ROOT = pathlib.Path(__file__).resolve().parents[3]
SESSION = {"urmail.user_id": "", "role": ""}


def current_setting(key, missing_ok=None):
    return SESSION.get(key, "")


con = sqlite3.connect(":memory:")
con.row_factory = sqlite3.Row
con.execute("PRAGMA foreign_keys=ON")
con.create_function("current_setting", 2, current_setting)
for f in ["0001_init.sql", "0002_write_side_effects.sql"]:
    con.executescript((ROOT / "migrations_sqlite" / f).read_text())

NOW = con.execute("SELECT CAST(strftime('%s','now') AS INTEGER)*1000000").fetchone()[0]
HOUR = 3600 * 1000000
DAY = 24 * HOUR
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


def rows(sql, *args):
    return con.execute(sql, args).fetchall()


SQL = {n: go_const(n) for n in [
    "marketingSessionSQL", "marketingAdminSQL", "marketingWorkerSQL",
    "marketingMailAllowedSQL", "marketingLimitsSQL", "marketingTimezoneSQL",
    "marketingChannelSQL", "marketingSenderSQL", "marketingSenderReadySQL",
    "marketingLatestWindowSQL", "marketingInsertWindowSQL", "marketingConsumeWindowSQL",
    "marketingCampaignForScheduleSQL", "marketingCampaignSnapshotSQL",
    "marketingEnqueueRecipientsSQL", "marketingFlowPositionSQL",
    "marketingQuotaReleaseSQL", "marketingFlowRetrySQL", "marketingFlowRetrySelectSQL",
    "marketingFlowDefinitionSQL", "marketingFlowAuditInsertSQL",
    "marketingRecipientSQL", "marketingCampaignRunSQL", "marketingRecipientSuppressSQL",
    "marketingRecipientSubmitSQL", "marketingCampaignRunningSQL", "marketingSuppressionHitSQL",
    "marketingRecipientFinishSQL", "marketingSuppressionBounceSQL",
    "marketingRecipientUncertainSQL", "marketingCampaignStatusSQL", "marketingAttemptResolveSQL",
    "marketingFeedbackChannelSQL", "marketingFeedbackRecipientSQL", "marketingFeedbackPriorSQL",
    "marketingFeedbackInsertSQL", "marketingSuppressionUpsertSQL", "marketingRecipientBouncedSQL",
    "marketingRecipientRevokedSQL", "marketingRecipientSkippedSQL",
    "marketingFlowClearReservationSQL", "marketingFlowActiveSQL", "marketingFlowStepExistsSQL",
    "marketingFlowStepUpdateSQL", "marketingFlowStepReadSQL", "marketingFlowAdvanceSQL",
    "marketingFlowEntrySuppressedSQL", "marketingFlowReserveSQL",
]}

DAY_LIMIT = re.compile(r"^[1-9][0-9]{0,8}$")
RATE_LIMIT = re.compile(r"^[1-9][0-9]{0,5}$")


def settings(scope, scope_id, key, value):
    con.execute("""INSERT INTO settings(scope,scope_id,key,value) VALUES(?,?,?,?)
 ON CONFLICT(scope,scope_id,key) DO UPDATE SET value=excluded.value""",
                (scope, scope_id, key, value))


# ------------------------------------------------------------------ fixtures
con.execute("INSERT INTO brands(id,slug,name) VALUES(1,'b','Brand')")
con.execute("INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(1,1,'T','t','active')")
for uid, role in [(1, "owner"), (2, "member")]:
    con.execute("INSERT INTO users(id,email,password_hash) VALUES(?,?,'h')", (uid, f"u{uid}@t.test"))
    con.execute("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(?,1,?)", (uid, role))
con.execute("INSERT INTO domains(id,tenant_id,fqdn,verification_token) VALUES(1,1,'t.test','tok')")
con.execute("UPDATE domains SET status='active',verified_at=?,provisioning_state='ready' WHERE id=1", (NOW,))
con.execute("""INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash)
 VALUES(1,1,1,'news','news@t.test','h')""")
con.execute("INSERT INTO marketing_lists(id,tenant_id,name) VALUES(1,1,'L')")
con.execute("""INSERT INTO marketing_channels(id,tenant_id,name,kind,sender_address,encrypted_config,enabled)
 VALUES(1,1,'C','dedicated_gateway','news@t.test','enc:v1:x',1)""")
con.execute("""INSERT INTO marketing_templates(id,tenant_id,name,subject,text_body)
 VALUES(1,1,'tpl','Subject','Body')""")
for email, opted in [("a@x.test", 1), ("b@x.test", 1), ("c@x.test", 0)]:
    con.execute("INSERT INTO marketing_contacts(tenant_id,list_id,email,consent_source,opted_in) VALUES(1,1,?,?,?)",
                (email, "test", opted))
con.execute("""INSERT INTO marketing_campaigns(id,tenant_id,name,channel_id,template_id,list_id)
 VALUES(1,1,'c',1,1,1)""")
settings("tenant", 1, "marketing_daily_recipient_limit", '"3"')
settings("tenant", 1, "marketing_rate_per_minute", '"60"')
settings("tenant", 1, "business_timezone", '"UTC"')


def admin(tenant=1, uid=1):
    SESSION["urmail.user_id"] = str(uid)
    SESSION["role"] = ""
    return bool(one(SQL["marketingAdminSQL"], tenant))


def worker(role="urmail_platform"):
    SESSION["role"] = role


check("admin gate accepts owner", admin(), True)
check("admin gate rejects member", admin(uid=2), False)
check("admin gate rejects unbound session", one(SQL["marketingAdminSQL"], 1), False)
worker()
check("worker gate accepts urmail_platform", one(SQL["marketingWorkerSQL"]), 1)
worker("")
check("worker gate rejects empty role", one(SQL["marketingWorkerSQL"]), 0)
check("worker gate rejects tenant role", bool(one(SQL["marketingWorkerSQL"])), False)
check("mail allowed needs active tenant", one(SQL["marketingMailAllowedSQL"], 1), 1)
con.execute("UPDATE domains SET provisioning_state='pending' WHERE id=1")
check("mail allowed needs a ready domain", one(SQL["marketingMailAllowedSQL"], 1), 0)
con.execute("UPDATE domains SET provisioning_state='ready' WHERE id=1")

# ------------------------------------------------------------------- limits
day_text, rate_text = one(SQL["marketingLimitsSQL"], 1)
check("limits read tenant JSON strings", (day_text, rate_text), ("3", "60"))
check("day limit pattern", bool(DAY_LIMIT.match(day_text)), True)
check("rate limit pattern", bool(RATE_LIMIT.match(rate_text)), True)
con.execute("DELETE FROM settings WHERE key='marketing_rate_per_minute'")
settings("platform", 0, "marketing_rate_per_minute", '"120"')
check("limits fall back to platform scope", one(SQL["marketingLimitsSQL"], 1)[1], "120")
settings("platform", 0, "marketing_rate_per_minute", '"0x"')
d, r = one(SQL["marketingLimitsSQL"], 1)
check("bad rate limit disables both", (d is None or not RATE_LIMIT.match(r)), True)
settings("tenant", 1, "marketing_rate_per_minute", '"60"')

# ----------------------------------------------------------------- timezone
check("timezone tenant wins", one(SQL["marketingTimezoneSQL"], 1), "UTC")
settings("platform", 0, "business_timezone", '"Asia/Tokyo"')
check("timezone falls back to platform", one(SQL["marketingTimezoneSQL"], 2), "Asia/Tokyo")

# ------------------------------------------------------------- quota window
DAY_LIMIT_V, RATE_LIMIT_V = 3, 60


def next_boundary(micros, tz):
    # PG: date_trunc('day', now() AT TIME ZONE tz) + 1 day, AT TIME ZONE tz.
    if tz == "UTC":
        return (micros // DAY + 1) * DAY
    raise AssertionError(tz)


def reserve_send(tenant, now, day_limit=DAY_LIMIT_V, rate_limit=RATE_LIMIT_V, tz="UTC"):
    row = con.execute(SQL["marketingLatestWindowSQL"], (tenant,)).fetchone()
    wid, ends, wz, used, nxt = (0, 0, "", 0, -2**63) if row is None else tuple(row)
    if wid == 0 or ends <= now:
        start = now
        boundary = next_boundary(now, tz)
        if wid != 0 and wz != tz and boundary < start + DAY:
            boundary = next_boundary(start + DAY, tz)
        wid = con.execute(SQL["marketingInsertWindowSQL"], (tenant, start, boundary, tz)).fetchone()[0]
        used, nxt = 0, -2**63
    if used >= day_limit:
        return "quota_exhausted", 0
    if nxt > now:
        return "rate_limited", 0
    con.execute(SQL["marketingConsumeWindowSQL"], (tenant, wid, now + round(60000000.0 / rate_limit)))
    return "", wid


check("quota window default is int64 min (not text -infinity)",
      one("SELECT typeof(next_send_at)||':'||next_send_at FROM marketing_quota_windows") , None)
status, wid = reserve_send(1, NOW)
check("first send opens a window", (status, wid > 0), ("", True))
check("window default next_send_at is int64 min",
      one("SELECT typeof(next_send_at) FROM marketing_quota_windows WHERE id=?", wid), "integer")
check("window used_count incremented",
      one("SELECT used_count FROM marketing_quota_windows WHERE id=?", wid), 1)
check("window ends at next local midnight",
      one("SELECT ends_at-(ends_at/86400000000)*86400000000 FROM marketing_quota_windows WHERE id=?", wid), 0)
check("rate limited immediately after send", reserve_send(1, NOW + 1)[0], "rate_limited")
status, wid2 = reserve_send(1, NOW + 1000001)
check("same window reused after the rate interval", (status, wid2), ("", wid))
check("second send counted", one("SELECT used_count FROM marketing_quota_windows WHERE id=?", wid), 2)
reserve_send(1, NOW + 2000002)
check("day limit exhausted", reserve_send(1, NOW + 3000003)[0], "quota_exhausted")
con.execute("UPDATE marketing_quota_windows SET ends_at=? WHERE id=?", (NOW + 4000004, wid))
status, wid3 = reserve_send(1, NOW + 5000005)
check("closed window rolls over", (status, wid3 != wid), ("", True))
check("rollover resets usage",
      one("SELECT used_count FROM marketing_quota_windows WHERE id=?", wid3), 1)

# ------------------------------------------------------------------- channel
check("channel check accepts dedicated gateway", one(SQL["marketingChannelSQL"], 1, 1), 1)
check("channel check requires the sender mailbox", one(SQL["marketingSenderSQL"], 1, 1), 1)
check("flow sender needs verified_at", one(SQL["marketingSenderReadySQL"], 1, 1), 1)
# domains CHECK (status<>'active' OR verified_at IS NOT NULL) already makes the
# verified_at clause of urmail_domain_mail_ready redundant in SQLite; prove the
# status half still gates the flow sender.
con.execute("UPDATE domains SET status='pending_dns' WHERE id=1")
check("flow sender rejects a non-active domain", one(SQL["marketingSenderReadySQL"], 1, 1), 0)
con.execute("UPDATE domains SET status='active' WHERE id=1")
con.execute("UPDATE marketing_channels SET enabled=0 WHERE id=1")
check("disabled channel rejected", one(SQL["marketingChannelSQL"], 1, 1), 0)
con.execute("UPDATE marketing_channels SET enabled=1 WHERE id=1")

# ------------------------------------------------------------------ schedule
admin()


def schedule(tenant, campaign, at, uid=1):
    SESSION["urmail.user_id"] = str(uid)
    if not one(SQL["marketingAdminSQL"], tenant):
        return "MARKETING_FORBIDDEN"
    row = con.execute(SQL["marketingCampaignForScheduleSQL"], (tenant, campaign)).fetchone()
    if row is None:
        return "not_found"
    status_, channel, template, list_ = tuple(row)
    if status_ != "draft":
        return status_
    if at is None:
        return "invalid_schedule"
    if not one(SQL["marketingMailAllowedSQL"], tenant):
        return "entitlement_denied"
    day_, rate_ = one(SQL["marketingLimitsSQL"], tenant)
    if not (day_ and rate_ and DAY_LIMIT.match(day_) and RATE_LIMIT.match(rate_)):
        return "quota_unconfigured"
    if not one(SQL["marketingChannelSQL"], tenant, channel):
        return "channel_unavailable"
    if not one(SQL["marketingSenderSQL"], tenant, channel):
        return "sender_unavailable"
    con.execute(SQL["marketingCampaignSnapshotSQL"], (tenant, campaign, at))
    con.execute(SQL["marketingEnqueueRecipientsSQL"], (tenant, campaign, list_))
    return "scheduled"


check("member cannot schedule", schedule(1, 1, NOW, uid=2), "MARKETING_FORBIDDEN")
check("missing campaign", schedule(1, 99, NOW), "not_found")
check("schedule requires a time", schedule(1, 1, None), "invalid_schedule")
check("schedule succeeds", schedule(1, 1, NOW + HOUR), "scheduled")
check("snapshot copied from the template",
      one("SELECT subject_snapshot||':'||body_snapshot FROM marketing_campaigns WHERE id=1"),
      "Subject:Body")
check("campaign scheduled", one("SELECT status FROM marketing_campaigns WHERE id=1"), "scheduled")
check("recipients enqueued in bulk",
      sorted(r[0] for r in rows("SELECT email FROM marketing_recipients WHERE campaign_id=1")),
      ["a@x.test", "b@x.test"])
check("opted-out contact excluded",
      one("SELECT COUNT(*) FROM marketing_recipients WHERE email='c@x.test'"), 0)
check("re-schedule keeps the existing status",
      schedule(1, 1, NOW + 2 * HOUR), "scheduled")
check("enqueue is idempotent",
      one("SELECT COUNT(*) FROM marketing_recipients WHERE campaign_id=1"), 2)
# The dispatch scenarios need a campaign whose schedule already elapsed, a
# fresh quota window and enough headroom for every scenario.
con.execute("UPDATE marketing_campaigns SET scheduled_at=? WHERE id=1", (NOW - HOUR,))
con.execute("DELETE FROM marketing_quota_windows")
settings("tenant", 1, "marketing_daily_recipient_limit", '"100"')
settings("tenant", 1, "marketing_rate_per_minute", '"60"')
CLOCK = [NOW]


def tick(micros=2 * 1000000):
    CLOCK[0] += micros
    return CLOCK[0]
SEQ = [2]


def new_recipient(email, status="queued"):
    SEQ[0] += 1
    con.execute("""INSERT INTO marketing_contacts(tenant_id,list_id,email,consent_source,opted_in)
 VALUES(1,1,?,'test',1) ON CONFLICT(tenant_id,list_id,email) DO UPDATE SET opted_in=1""", (email,))
    con.execute("""INSERT INTO marketing_recipients(tenant_id,campaign_id,email,status,next_attempt_at)
 VALUES(1,1,?,?,?)""", (email, status, NOW))
    return one("SELECT id FROM marketing_recipients WHERE campaign_id=1 AND email=?", email)


def lease(recipient, owner="w1", until=None, fence=1, status="leased"):
    con.execute("UPDATE marketing_recipients SET status=?,lease_owner=?,lease_until=?,fence=? WHERE id=?",
                (status, owner, until if until is not None else NOW + HOUR, fence, recipient))


def authorize(tenant, recipient, owner, fence, now=None, role="urmail_platform"):
    now = tick() if now is None else now
    worker(role)
    if not one(SQL["marketingWorkerSQL"]):
        return "MARKETING_WORKER_REQUIRED"
    r = con.execute(SQL["marketingRecipientSQL"], (tenant, recipient)).fetchone()
    if r is None:
        return "stale"
    if r["status"] != "leased" or r["lease_owner"] != owner or r["fence"] != fence \
            or r["lease_until"] is None or r["lease_until"] <= now:
        return "stale"
    c = con.execute(SQL["marketingCampaignRunSQL"], (tenant, r["campaign_id"])).fetchone()
    if (c["status"] not in ("scheduled", "running")) or c["scheduled_at"] > now:
        return "paused"
    if one(SQL["marketingSuppressionHitSQL"], tenant, r["email"], c["list_id"]):
        con.execute(SQL["marketingRecipientSuppressSQL"], (tenant, recipient, now))
        return "suppressed"
    if not one(SQL["marketingMailAllowedSQL"], tenant):
        return "entitlement_denied"
    day_, rate_ = one(SQL["marketingLimitsSQL"], tenant)
    if not (day_ and rate_ and DAY_LIMIT.match(day_) and RATE_LIMIT.match(rate_)):
        return "quota_unconfigured"
    if not one(SQL["marketingChannelSQL"], tenant, c["channel_id"]):
        return "channel_unavailable"
    if not one(SQL["marketingSenderSQL"], tenant, c["channel_id"]):
        return "sender_unavailable"
    status, wid = reserve_send(tenant, now, int(day_), int(rate_))
    if status:
        return status
    con.execute(SQL["marketingRecipientSubmitSQL"], (tenant, recipient, wid))
    con.execute(SQL["marketingCampaignRunningSQL"], (tenant, r["campaign_id"]))
    return "authorized"


RID = new_recipient("r1@x.test")
lease(RID)
check("worker gate blocks a foreign role", authorize(1, RID, "w1", 1, role="x"),
      "MARKETING_WORKER_REQUIRED")
lease(RID, fence=5)
check("stale fence", authorize(1, RID, "w1", 1), "stale")
lease(RID, until=NOW - 1, fence=1)
check("expired lease", authorize(1, RID, "w1", 1), "stale")
lease(RID)
check("future scheduled campaign is paused",
      authorize(1, RID, "w1", 1, now=NOW - 2 * HOUR), "paused")
EMAIL1 = one("SELECT email FROM marketing_recipients WHERE id=?", RID)
con.execute("INSERT INTO marketing_suppressions(tenant_id,email,reason) VALUES(1,?,'manual')", (EMAIL1,))
check("suppressed recipient", authorize(1, RID, "w1", 1), "suppressed")
check("suppressed recipient recorded",
      one("SELECT status||':'||result_code FROM marketing_recipients WHERE id=?", RID), "suppressed:suppressed")
con.execute("DELETE FROM marketing_suppressions")
con.execute("UPDATE marketing_contacts SET opted_in=0 WHERE email=?", (EMAIL1,))
lease(RID)
check("opted-out recipient is suppressed", authorize(1, RID, "w1", 1), "suppressed")
con.execute("UPDATE marketing_contacts SET opted_in=1 WHERE email=?", (EMAIL1,))
lease(RID)
check("authorize reserves a slot", authorize(1, RID, "w1", 1), "authorized")
check("recipient submitting with window",
      one("SELECT status||':'||reserved||':'||(window_id IS NOT NULL) FROM marketing_recipients WHERE id=?", RID),
      "submitting:1:1")
check("campaign running", one("SELECT status FROM marketing_campaigns WHERE id=1"), "running")
lease(RID, status="submitting")
check("authorize needs a leased row", authorize(1, RID, "w1", 1), "stale")

# -------------------------------------------------------------------- finish
def finish(tenant, recipient, owner, fence, result, now=None):
    now = tick() if now is None else now
    worker()
    if result not in ("sent", "uncertain", "failed", "bounced"):
        return "MARKETING_WORKER_REQUIRED"
    r = con.execute(SQL["marketingRecipientSQL"], (tenant, recipient)).fetchone()
    if r is None:
        return False
    if r["status"] != "submitting" or r["lease_owner"] != owner or r["fence"] != fence \
            or r["lease_until"] is None or r["lease_until"] <= now:
        return False
    if result in ("failed", "bounced") and r["reserved"] and r["window_id"] is not None:
        con.execute(SQL["marketingQuotaReleaseSQL"], (tenant, r["window_id"]))
    backoff = 1 << min(r["attempts"], 10)
    con.execute(SQL["marketingRecipientFinishSQL"], (tenant, recipient, result, now, now + backoff * 1000000))
    if result == "bounced":
        con.execute(SQL["marketingSuppressionBounceSQL"], (tenant, r["email"]))
    return True


RID = new_recipient("r2@x.test")
lease(RID)
authorize(1, RID, "w1", 1)
con.execute("UPDATE marketing_quota_windows SET used_count=2 WHERE tenant_id=1 AND used_count=1")
before = one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1")
check("finish rejects an unknown result", finish(1, RID, "w1", 1, "weird"), "MARKETING_WORKER_REQUIRED")
check("finish rejects a stale fence", finish(1, RID, "w1", 99, "sent"), False)
con.execute("UPDATE marketing_recipients SET attempts=1 WHERE id=?", (RID,))
check("finish sent", finish(1, RID, "w1", 1, "sent"), True)
check("sent keeps the quota slot",
      one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1"), before)
check("sent completion",
      one("SELECT status||':'||result_code||':'||(completed_at IS NOT NULL) FROM marketing_recipients WHERE id=?", RID),
      "sent:sent:1")
check("sent keeps reserved",
      one("SELECT reserved FROM marketing_recipients WHERE id=?", RID), 1)

RID = new_recipient("r3@x.test")
lease(RID)
authorize(1, RID, "w1", 1)
con.execute("UPDATE marketing_recipients SET attempts=1 WHERE id=?", (RID,))
used = one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1")
check("finish failed", finish(1, RID, "w1", 1, "failed"), True)
check("failed refunds the slot",
      one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1"), used - 1)
check("failed requeues under 5 attempts",
      one("SELECT status||':'||(completed_at IS NULL) FROM marketing_recipients WHERE id=?", RID), "queued:1")
check("failed backoff is 2^attempts",
      one("SELECT (next_attempt_at-?)=2000000 FROM marketing_recipients WHERE id=?", CLOCK[0], RID), 1)
con.execute("UPDATE marketing_recipients SET status='submitting',lease_until=?,attempts=5 WHERE id=?", (NOW + HOUR, RID))
check("five failures stop retrying", finish(1, RID, "w1", 1, "failed"), True)
check("exhausted failure is final",
      one("SELECT status||':'||(completed_at IS NOT NULL)||':'||reserved FROM marketing_recipients WHERE id=?", RID),
      "failed:1:0")

RID = new_recipient("r4@x.test")
lease(RID)
authorize(1, RID, "w1", 1)
check("finish bounced", finish(1, RID, "w1", 1, "bounced"), True)
check("bounce suppression inserted",
      one("SELECT reason FROM marketing_suppressions WHERE tenant_id=1 AND email=(SELECT email FROM marketing_recipients WHERE id=?)", RID),
      "bounce")
check("bounce clears reserved",
      one("SELECT reserved FROM marketing_recipients WHERE id=?", RID), 0)

# ---------------------------------------------------------------- resolution
def resolve(tenant, recipient, outcome, reason, now=None):
    now = tick() if now is None else now
    worker()
    if outcome not in ("accepted", "not_submitted") or not (1 <= len(reason.strip()) <= 2048):
        return "MARKETING_RESOLUTION_INVALID"
    r = con.execute(SQL["marketingRecipientUncertainSQL"], (tenant, recipient)).fetchone()
    if r is None or r["status"] != "uncertain":
        return False
    campaign = one(SQL["marketingCampaignStatusSQL"], tenant, r["campaign_id"])
    if outcome == "not_submitted" and r["reserved"] and r["window_id"] is not None:
        con.execute(SQL["marketingQuotaReleaseSQL"], (tenant, r["window_id"]))
    con.execute(SQL["marketingAttemptResolveSQL"], (tenant, recipient, outcome, campaign, reason, now))
    return True


RID = new_recipient("r5@x.test")
lease(RID)
authorize(1, RID, "w1", 1)
con.execute("UPDATE marketing_recipients SET status='uncertain' WHERE id=?", (RID,))
check("resolve rejects a short reason", resolve(1, RID, "accepted", "  "), "MARKETING_RESOLUTION_INVALID")
check("resolve rejects an unknown outcome", resolve(1, RID, "maybe", "operator note"), "MARKETING_RESOLUTION_INVALID")
used = one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1")
check("resolve accepted", resolve(1, RID, "accepted", "provider accepted it"), True)
check("accepted becomes sent",
      one("SELECT status||':'||result_code FROM marketing_recipients WHERE id=?", RID),
      "sent:manually_confirmed_accepted")
check("accepted keeps the slot",
      one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1"), used)
check("resolve clears the lease",
      one("SELECT (lease_owner IS NULL)||':'||(lease_until IS NULL)||':'||fence FROM marketing_recipients WHERE id=?", RID),
      "1:1:2")
check("resolve on a non-uncertain row is false", resolve(1, RID, "accepted", "again"), False)

con.execute("UPDATE marketing_recipients SET status='uncertain',reserved=1,window_id=(SELECT id FROM marketing_quota_windows ORDER BY id DESC LIMIT 1) WHERE id=?", (RID,))
used = one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1")
check("resolve not_submitted", resolve(1, RID, "not_submitted", "no SMTP session"), True)
check("not_submitted refunds the slot",
      one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1"), used - 1)
check("not_submitted requeues",
      one("SELECT status FROM marketing_recipients WHERE id=?", RID), "queued")

con.execute("UPDATE marketing_campaigns SET status='canceled' WHERE id=1")
con.execute("UPDATE marketing_recipients SET status='uncertain',reserved=0 WHERE id=?", (RID,))
check("resolve on a canceled campaign", resolve(1, RID, "not_submitted", "canceled meanwhile"), True)
check("canceled campaign parks the recipient",
      one("SELECT status FROM marketing_recipients WHERE id=?", RID), "canceled")
con.execute("UPDATE marketing_campaigns SET status='running' WHERE id=1")

# ------------------------------------------------------------------ feedback
def feedback(channel, event, recipient, status, payload_hash):
    worker()
    if status not in ("bounced", "complaint", "deferred") or not (1 <= len(event) <= 200) \
            or not re.match(r"^[a-f0-9]{64}$", payload_hash):
        return "MARKETING_FEEDBACK_INVALID"
    tid = one(SQL["marketingFeedbackChannelSQL"], channel)
    if tid is None:
        return "not_found"
    r = one(SQL["marketingFeedbackRecipientSQL"], tid, recipient, channel)
    if r is None:
        return "not_found"
    st, email = r
    if st not in ("submitting", "sent", "uncertain", "bounced"):
        return "not_found"
    prior = one(SQL["marketingFeedbackPriorSQL"], channel, event)
    if prior is not None:
        return "duplicate" if prior == payload_hash else "conflict"
    con.execute(SQL["marketingFeedbackInsertSQL"], (channel, event, tid, recipient, status, payload_hash))
    if status in ("bounced", "complaint"):
        con.execute(SQL["marketingSuppressionUpsertSQL"], (tid, email, "bounce" if status == "bounced" else "complaint"))
        con.execute(SQL["marketingRecipientBouncedSQL"], (tid, recipient, status, NOW))
    return "recorded"


H1, H2 = "a" * 64, "b" * 64
check("feedback rejects a bad hash", feedback(1, "ev", 1, "bounced", "nope"), "MARKETING_FEEDBACK_INVALID")
check("feedback rejects an unknown status", feedback(1, "ev", 1, "opened", H1), "MARKETING_FEEDBACK_INVALID")
check("feedback rejects an empty event", feedback(1, "", 1, "bounced", H1), "MARKETING_FEEDBACK_INVALID")
check("feedback missing channel", feedback(99, "ev", 1, "bounced", H1), "not_found")
check("feedback wrong recipient", feedback(1, "ev", 999, "bounced", H1), "not_found")
FB = new_recipient("fb1@x.test", status="sent")
FD = new_recipient("fb2@x.test", status="sent")
check("feedback recorded", feedback(1, "ev1", FB, "bounced", H1), "recorded")
check("feedback duplicate", feedback(1, "ev1", FB, "bounced", H1), "duplicate")
check("feedback conflict", feedback(1, "ev1", FB, "bounced", H2), "conflict")
check("feedback bounce suppresses", one(SQL["marketingFeedbackPriorSQL"], 1, "ev1"), H1)
check("feedback bounce marks the recipient",
      one("SELECT status||':'||result_code FROM marketing_recipients WHERE id=?", FB), "bounced:bounced")
check("feedback deferred does not touch the recipient", feedback(1, "ev2", FD, "deferred", H1), "recorded")
check("deferred keeps recipient status",
      one("SELECT status FROM marketing_recipients WHERE id=?", FD), "sent")

# ------------------------------------------------------------- revoked/skip
def revoke(tenant, recipient, owner, fence):
    worker()
    r = con.execute(SQL["marketingRecipientSQL"], (tenant, recipient)).fetchone()
    if r is None or r["status"] != "submitting" or r["lease_owner"] != owner or r["fence"] != fence:
        return False
    if r["reserved"] and r["window_id"] is not None:
        con.execute(SQL["marketingQuotaReleaseSQL"], (tenant, r["window_id"]))
    con.execute(SQL["marketingRecipientRevokedSQL"], (tenant, recipient, NOW))
    return True


RID = new_recipient("r6@x.test")
lease(RID, status="submitting")
con.execute("UPDATE marketing_recipients SET reserved=1,window_id=(SELECT id FROM marketing_quota_windows ORDER BY id DESC LIMIT 1) WHERE id=?", (RID,))
used = one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1")
check("revoke rejects a stale fence", revoke(1, RID, "w1", 9), False)
check("revoke succeeds", revoke(1, RID, "w1", 1), True)
check("revoke refunds the slot",
      one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1"), used - 1)
check("revoke suppresses the recipient",
      one("SELECT status||':'||result_code FROM marketing_recipients WHERE id=?", RID), "suppressed:unsubscribed_mid_run")


def skip(tenant, recipient, owner, fence, reason):
    worker()
    if reason not in ("entitlement_denied", "quota_unconfigured", "channel_unavailable",
                      "sender_unavailable", "timezone_invalid"):
        return "MARKETING_WORKER_REQUIRED"
    r = con.execute(SQL["marketingRecipientSQL"], (tenant, recipient)).fetchone()
    if r is None or r["status"] != "leased" or r["lease_owner"] != owner or r["fence"] != fence:
        return False
    if r["reserved"] and r["window_id"] is not None:
        con.execute(SQL["marketingQuotaReleaseSQL"], (tenant, r["window_id"]))
    con.execute(SQL["marketingRecipientSkippedSQL"], (tenant, recipient, reason, NOW))
    return True


RID = new_recipient("r7@x.test")
lease(RID)
check("skip rejects an unknown reason", skip(1, RID, "w1", 1, "because"), "MARKETING_WORKER_REQUIRED")
check("skip succeeds", skip(1, RID, "w1", 1, "quota_unconfigured"), True)
check("skip cancels the recipient",
      one("SELECT status||':'||result_code FROM marketing_recipients WHERE id=?", RID), "canceled:quota_unconfigured")

# --------------------------------------------------------------------- flow
con.execute("INSERT INTO marketing_flows(id,tenant_id,name,status) VALUES(1,1,'f','active')")
con.execute("""INSERT INTO marketing_flow_definitions(id,tenant_id,name,draft) VALUES(1,1,'d','{}')""")
con.execute("""INSERT INTO marketing_flow_steps(tenant_id,flow_id,step_key,kind,position)
 VALUES(1,1,'start','send',0)""")
con.execute("""INSERT INTO marketing_flow_steps(tenant_id,flow_id,step_key,kind,position)
 VALUES(1,1,'end','stop',1)""")
con.execute("""INSERT INTO marketing_flow_versions(id,tenant_id,definition_id,version,draft_revision,
 runtime_flow_id,definition,published_by) VALUES(1,1,1,1,1,1,'{}',1)""")
con.execute("""INSERT INTO marketing_flow_entries(id,tenant_id,definition_id,version_id,event_key,
 source,list_id,recipient_count) VALUES(1,1,1,1,'contact_enrolled','contact_enrolled',1,1)""")
con.execute("""INSERT INTO marketing_flow_positions(id,tenant_id,flow_id,email,step_key,entry_id,event_facts)
 VALUES(1,1,1,'a@x.test','start',1,'{}')""")
FP = "a@x.test"


def flow_advance(tenant, flow, email):
    return one(SQL["marketingFlowAdvanceSQL"], tenant, flow, email)


def commit_step(tenant, flow, email, event, next_key, now=None):
    now = now or NOW
    r = con.execute(SQL["marketingFlowStepReadSQL"], (tenant, flow, email)).fetchone()
    if r is None:
        return "missing"
    if r["delivery_uncertain"]:
        return "uncertain"
    if r["unsubscribed"] or (r["wait_until"] is not None and r["wait_until"] > now) or not flow_advance(tenant, flow, email):
        return "blocked"
    unsub = 1 if event == "unsubscribed" else r["unsubscribed"]
    step = next_key or r["step_key"]
    con.execute(SQL["marketingFlowStepUpdateSQL"], (tenant, flow, email, step, unsub, now))
    return step


check("flow may advance", flow_advance(1, 1, FP), 1)
check("step commit advances", commit_step(1, 1, FP, "", "end"), "end")
check("step stored", one("SELECT step_key FROM marketing_flow_positions WHERE id=1"), "end")
check("missing position", commit_step(1, 1, "nope@x.test", "", ""), "missing")
check("unconsumed step with empty next stays", commit_step(1, 1, FP, "", ""), "end")
con.execute("UPDATE marketing_flow_positions SET wait_until=? WHERE id=1", (NOW + HOUR,))
check("wait blocks the step", commit_step(1, 1, FP, "", "end"), "blocked")
con.execute("UPDATE marketing_flow_positions SET wait_until=NULL WHERE id=1")
check("unsubscribe advances", commit_step(1, 1, FP, "unsubscribed", "end"), "end")
check("unsubscribe recorded", one("SELECT unsubscribed FROM marketing_flow_positions WHERE id=1"), 1)
check("unsubscribed blocks further advance", commit_step(1, 1, FP, "", "end"), "blocked")
con.execute("UPDATE marketing_flow_positions SET unsubscribed=0,delivery_uncertain=1 WHERE id=1")
check("uncertain blocks the step", commit_step(1, 1, FP, "", "end"), "uncertain")
check("suppression blocks advance",
      (con.execute("INSERT INTO marketing_suppressions(tenant_id,email,reason) VALUES(1,?, 'manual')", (FP,)),
       flow_advance(1, 1, FP))[1], 0)
con.execute("DELETE FROM marketing_suppressions")
con.execute("UPDATE marketing_flow_positions SET delivery_uncertain=0 WHERE id=1")


def begin_flow(tenant, flow, email, owner, fence, channel, now=None):
    now = tick() if now is None else now
    worker()
    r = con.execute(SQL["marketingFlowPositionSQL"], (tenant, flow, email)).fetchone()
    if r is None:
        return "stale_lease"
    if r["lease_owner"] != owner or r["lease_fence"] != fence or r["lease_until"] is None or r["lease_until"] <= now:
        return "stale_lease"
    if r["delivery_uncertain"] or r["flow_reserved"]:
        return "uncertain"
    if r["stopped_at"] is not None or r["unsubscribed"] or not one(SQL["marketingFlowActiveSQL"], tenant, flow):
        return "paused"
    if one(SQL["marketingFlowEntrySuppressedSQL"], tenant, email, r["entry_id"]):
        return "suppressed"
    if not one(SQL["marketingMailAllowedSQL"], tenant):
        return "entitlement_denied"
    day_, rate_ = one(SQL["marketingLimitsSQL"], tenant)
    if not (day_ and rate_ and DAY_LIMIT.match(day_) and RATE_LIMIT.match(rate_)):
        return "quota_unconfigured"
    if not one(SQL["marketingChannelSQL"], tenant, channel):
        return "channel_unavailable"
    if not one(SQL["marketingSenderReadySQL"], tenant, channel):
        return "sender_unavailable"
    status, wid = reserve_send(tenant, now, int(day_), int(rate_))
    if status:
        return status
    con.execute(SQL["marketingFlowReserveSQL"], (tenant, flow, email, wid, now))
    return "authorized"


con.execute("UPDATE marketing_flow_positions SET lease_owner='fw',lease_fence=3,lease_until=? WHERE id=1", (NOW + HOUR,))
check("flow begin needs the lease", begin_flow(1, 1, FP, "other", 3, 1), "stale_lease")
check("flow begin reserves", begin_flow(1, 1, FP, "fw", 3, 1), "authorized")
check("flow reservation recorded",
      one("SELECT delivery_uncertain||':'||flow_reserved||':'||(flow_window_id IS NOT NULL) FROM marketing_flow_positions WHERE id=1"),
      "1:1:1")
check("flow begin twice is uncertain", begin_flow(1, 1, FP, "fw", 3, 1), "uncertain")
check("flow stop pauses", (con.execute("UPDATE marketing_flow_positions SET delivery_uncertain=0,flow_reserved=0,flow_window_id=NULL,stopped_at=? WHERE id=1", (NOW,)),
                           begin_flow(1, 1, FP, "fw", 3, 1))[1], "paused")
con.execute("UPDATE marketing_flow_positions SET stopped_at=NULL WHERE id=1")
con.execute("INSERT INTO marketing_suppressions(tenant_id,email,reason) VALUES(1,?,'manual')", (FP,))
check("flow begin suppressed", begin_flow(1, 1, FP, "fw", 3, 1), "suppressed")
con.execute("DELETE FROM marketing_suppressions")


def release(tenant, flow, email, owner, fence, now=None):
    now = tick() if now is None else now
    con.execute("UPDATE marketing_flow_positions SET delivery_uncertain=1,flow_reserved=1,flow_window_id=(SELECT id FROM marketing_quota_windows ORDER BY id DESC LIMIT 1) WHERE id=1")
    r = con.execute(SQL["marketingFlowPositionSQL"], (tenant, flow, email)).fetchone()
    if r is None or r["lease_owner"] != owner or r["lease_fence"] != fence \
            or r["lease_until"] is None or r["lease_until"] <= now:
        return False
    if r["flow_reserved"] and r["flow_window_id"] is not None:
        con.execute(SQL["marketingQuotaReleaseSQL"], (tenant, r["flow_window_id"]))
    con.execute(SQL["marketingFlowClearReservationSQL"], (tenant, flow, email))
    return True


used = one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1")
check("flow release refuses a stale lease", release(1, 1, FP, "fw", 9), False)
check("flow release refunds", (release(1, 1, FP, "fw", 3),
      one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1"))[1], used - 1)
check("flow release clears flags",
      one("SELECT delivery_uncertain||':'||flow_reserved||':'||(flow_window_id IS NULL) FROM marketing_flow_positions WHERE id=1"),
      "0:0:1")


def retry(tenant, definition, position, confirmed, now=None):
    now = tick() if now is None else now
    r = con.execute(SQL["marketingFlowRetrySelectSQL"], (tenant, position, definition)).fetchone()
    if r is None:
        return False
    if r["stopped_at"] is not None or r["unsubscribed"] or (r["lease_until"] is not None and r["lease_until"] > now) \
            or (not r["delivery_uncertain"] and r["last_error"] == "") \
            or (r["delivery_uncertain"] and not confirmed):
        return False
    if r["flow_reserved"] and r["flow_window_id"] is not None:
        con.execute(SQL["marketingQuotaReleaseSQL"], (tenant, r["flow_window_id"]))
    con.execute(SQL["marketingFlowRetrySQL"], (tenant, r["id"], now))
    return True


check("retry needs a failed or uncertain position", retry(1, 1, 1, False), False)
con.execute("UPDATE marketing_flow_positions SET lease_until=NULL,last_error='boom',delivery_uncertain=0 WHERE id=1")
check("retry a failed position", retry(1, 1, 1, False), True)
check("retry clears the error and lease",
      one("SELECT last_error||':'||(lease_owner IS NULL)||':'||attempt_count FROM marketing_flow_positions WHERE id=1"),
      ":1:0")
con.execute("UPDATE marketing_flow_positions SET delivery_uncertain=1,flow_reserved=1,flow_window_id=(SELECT id FROM marketing_quota_windows ORDER BY id DESC LIMIT 1)")
used = one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1")
check("uncertain retry needs confirmation", retry(1, 1, 1, False), False)
check("uncertain retry with confirmation", retry(1, 1, 1, True), True)
check("retry refunds the reservation",
      one("SELECT used_count FROM marketing_quota_windows ORDER BY id DESC LIMIT 1"), used - 1)
check("retry against a foreign definition", retry(1, 2, 1, True), False)

# -------------------------------------------------------------------- audit
con.execute("DROP TRIGGER audit_logs_append_only_insert")
SESSION["urmail.user_id"] = "1"
definition_ok = one(SQL["marketingFlowDefinitionSQL"], 1, 1)
check("audit action whitelist", definition_ok, 1)
con.execute(SQL["marketingFlowAuditInsertSQL"], (1, 1, "marketing.flow.published", "1", '{"x":1}'))
check("audit row written",
      one("SELECT action||':'||target_type||':'||target_id||':'||detail FROM audit_logs WHERE action LIKE 'marketing.flow.%'"),
      'marketing.flow.published:marketing_flow:1:{"x":1}')

print("fails:", failures)
raise SystemExit(1 if failures else 0)
