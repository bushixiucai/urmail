#!/usr/bin/env python3
"""S4 batch E probe: AI configuration procedures.

Drives the SQL constants of internal/store/sqlite_methods.go against a schema
built from migrations_sqlite, with the orchestration mirrored in Python (same
approach as batches B/C/D: the statement text under test is the Go const).

Covered: owner|admin session gates, mode/provider validation, the platform /
enterprise snapshot split, the guard trigger's version bump, the
configuration_digest port (including consent invalidation after a config
change), cloud consent grant/revoke, and the governance policy upsert.
"""
import hashlib
import json
import pathlib
import re
import sqlite3

ROOT = pathlib.Path(__file__).resolve().parents[3]
SESSION = {"urmail.tenant_id": "", "urmail.user_id": "", "role": ""}


def current_setting(key, missing_ok=None):
    return SESSION.get(key, "")


con = sqlite3.connect(":memory:")
con.row_factory = sqlite3.Row
con.execute("PRAGMA foreign_keys=ON")
con.create_function("current_setting", 2, current_setting)
for f in ["0001_init.sql", "0002_write_side_effects.sql"]:
    con.executescript((ROOT / "migrations_sqlite" / f).read_text())

failures = []


def check(label, got, want):
    if got == want:
        print(f"PASS {label}")
    else:
        failures.append(label)
        print(f"FAIL {label}: got {got!r} want {want!r}")


def go_const(name):
    src = (ROOT / "internal/store/sqlite_methods.go").read_text()
    m = re.search(r"const " + name + r" = `(.*?)`\n", src, re.S)
    if not m:
        raise KeyError(name)
    return m.group(1)


def one(sql, *args):
    row = con.execute(sql, args).fetchone()
    return None if row is None else (row[0] if len(row) == 1 else tuple(row))


def value(sql, *args):
    row = con.execute(sql, args).fetchone()
    return row[0]


SQL = {n: go_const(n) for n in [
    "sessionActorSQL", "tenantAdminSQL", "aiConfigExistsSQL", "aiConfigureSQL",
    "aiConsentConfigSQL", "aiConsentUpdateSQL", "aiGovernanceSQL",
    "aiConfigurationIdentitySQL", "aiConfigurationDigestSQL", "aiConsentResetSQL",
    "aiSetProviderSQL", "aiSetActiveServiceSQL",
]}

# --------------------------------------------------------------- digest port
def jsonb_string(s):
    out = '"""'
    for ch in s:
        if ch == '"""':
            out += '\\"""'
        elif ch == "\\":
            out += "\\\\"
        elif ch == "\b":
            out += "\\b"
        elif ch == "\f":
            out += "\\f"
        elif ch == "\n":
            out += "\\n"
        elif ch == "\r":
            out += "\\r"
        elif ch == "\t":
            out += "\\t"
        elif ord(ch) < 0x20:
            out += "\\u%04x" % ord(ch)
        else:
            out += ch
    return out + '"""'


def digest_of(version, mode, provider, endpoint, model, scope):
    payload = "[" + ", ".join([str(version)] + [jsonb_string(x) for x in
                                                (mode, provider, endpoint, model, scope)]) + "]"
    return hashlib.sha256(payload.encode()).hexdigest()


def refresh_digest(tenant):
    version, mode, provider, endpoint, model, scope = one(SQL["aiConfigurationIdentitySQL"], tenant)
    d = digest_of(version, mode, provider, endpoint, model, scope)
    con.execute(SQL["aiConfigurationDigestSQL"], (tenant, d))
    return d


# ---------------------------------------------------------------- fixtures
con.execute("INSERT INTO brands(id,slug,name) VALUES(1,'b','Brand')")
con.execute("INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(1,1,'T','t','active')")
for uid, role in [(1, "owner"), (2, "admin"), (3, "member")]:
    con.execute("INSERT INTO users(id,email,password_hash) VALUES(?,?,'h')", (uid, f"u{uid}@t.test"))
    con.execute("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(?,1,?)", (uid, role))

SCOPE_HASH = value("SELECT cloud_scope_hash FROM ai_provider_configs WHERE 0") if False else \
    "e2c6e77cf242166013d200a355f30a3c18f2798ae245d891d58c50ad8a57ab82"


def session(tenant=1, uid=1, role=""):
    SESSION["urmail.tenant_id"] = "" if tenant is None else str(tenant)
    SESSION["urmail.user_id"] = "" if uid is None else str(uid)
    SESSION["role"] = role


def actor():
    tid, uid, role = one(SQL["sessionActorSQL"])
    return tid, uid, role


def gate():
    tid, uid, role = actor()
    if tid is None or uid is None:
        return "AI_CONFIGURATION_FORBIDDEN"
    if not value(SQL["tenantAdminSQL"], tid):
        return "AI_CONFIGURATION_FORBIDDEN"
    return None


# ------------------------------------------------------------------ gates
session(tenant=None, uid=None)
check("actor is unbound without settings", actor(), (None, None, ""))
check("unbound session is forbidden", gate(), "AI_CONFIGURATION_FORBIDDEN")
session(uid=None)
check("missing user is forbidden", gate(), "AI_CONFIGURATION_FORBIDDEN")
session(tenant=None)
check("missing tenant is forbidden", gate(), "AI_CONFIGURATION_FORBIDDEN")
session(uid=3)
check("member is forbidden", gate(), "AI_CONFIGURATION_FORBIDDEN")
session(uid=1)
check("owner passes", gate(), None)
session(uid=2)
check("admin passes", gate(), None)
session(uid=1, role="urmail_platform")
check("actor also carries the session role", actor(), (1, 1, "urmail_platform"))
session(role="")
check("actor has no role for an app session", actor(), (1, 1, ""))


# --------------------------------------------------- configure_tenant_ai
def configure(mode, provider, endpoint, model, key, enabled, local_only, uid=1, tenant=1):
    session(tenant, uid)
    if mode not in ("byo", "local"):
        return "AI_CONFIGURATION_INVALID"
    if (mode == "local") != (provider == "ollama"):
        return "AI_CONFIGURATION_INVALID"
    if local_only and provider != "ollama":
        return "AI_CONFIGURATION_INVALID"
    err = gate()
    if err:
        return err
    tid, who, _ = actor()
    existed = bool(value(SQL["aiConfigExistsSQL"], tid))
    con.execute(SQL["aiConfigureSQL"], (tid, mode, provider, endpoint, model, key, int(enabled), int(local_only)))
    if existed and mode == "local":
        con.execute(SQL["aiConsentResetSQL"], (tid, who))
    refresh_digest(tid)
    return "ok"


check("reject platform mode", configure("platform", "openai", "", "m", "", 1, 0), "AI_CONFIGURATION_INVALID")
check("reject local with a remote provider", configure("local", "openai", "", "m", "", 1, 1), "AI_CONFIGURATION_INVALID")
check("reject byo with ollama", configure("byo", "ollama", "", "m", "", 1, 0), "AI_CONFIGURATION_INVALID")
check("reject local_only with openai", configure("byo", "openai", "", "m", "", 1, 1), "AI_CONFIGURATION_INVALID")
check("member cannot configure", configure("byo", "openai", "https://a.test/v1", "m", "k", 1, 0, uid=3),
      "AI_CONFIGURATION_FORBIDDEN")
check("configure byo", configure("byo", "openai", "https://a.test/v1", "gpt-4", "enc:v1:k1", 1, 0), "ok")
check("enterprise snapshot mirrors the base row",
      one("""SELECT enterprise_mode||':'||enterprise_provider||':'||enterprise_endpoint||':'||
 enterprise_model||':'||enterprise_encrypted_key||':'||enterprise_enabled||':'||active_service
 FROM ai_provider_configs WHERE tenant_id=1"""),
      "byo:openai:https://a.test/v1:gpt-4:enc:v1:k1:1:enterprise")
check("base row written", one("SELECT mode||':'||provider||':'||model FROM ai_provider_configs WHERE tenant_id=1"),
      "byo:openai:gpt-4")
check("platform snapshot untouched",
      one("SELECT platform_provider||':'||platform_endpoint||':'||platform_enabled FROM ai_provider_configs WHERE tenant_id=1"),
      "openai::0")
first = one("SELECT configuration_version,configuration_digest,tenant_local_only FROM ai_provider_configs WHERE tenant_id=1")
check("version starts at 1", first[0], 1)
check("digest matches identity", first[1],
      digest_of(1, "byo", "openai", "https://a.test/v1", "gpt-4", SCOPE_HASH))
check("fresh insert is local-only until consent", first[2], 1)

check("empty key keeps the previous enterprise key",
      (configure("byo", "openai", "https://a.test/v1", "gpt-4", "", 1, 0),
       one("SELECT encrypted_key||':'||enterprise_encrypted_key FROM ai_provider_configs WHERE tenant_id=1"))[1],
      "enc:v1:k1:enc:v1:k1")
check("identity change bumps the version",
      one("SELECT configuration_version FROM ai_provider_configs WHERE tenant_id=1"), 1)
check("model change bumps the version",
      (configure("byo", "openai", "https://a.test/v1", "gpt-4o", "", 1, 0),
       one("SELECT configuration_version FROM ai_provider_configs WHERE tenant_id=1"))[1], 2)
check("digest follows the bumped version",
      one("SELECT configuration_digest FROM ai_provider_configs WHERE tenant_id=1"),
      digest_of(2, "byo", "openai", "https://a.test/v1", "gpt-4o", SCOPE_HASH))
configure("byo", "openai", "https://a.test/v1", "gpt-4o", "", 1, 0)
check("repeating the same identity keeps the version",
      one("SELECT configuration_version FROM ai_provider_configs WHERE tenant_id=1"), 2)


# ------------------------------------------------------------ set_active_ai
def set_active(service, tenant=1):
    con.execute(SQL["aiSetActiveServiceSQL"], (service, tenant))
    refresh_digest(tenant)


# Seed the platform snapshot while the base row still points at the enterprise
# one, so the later switch is a real identity change.
configure("byo", "openai", "https://ent.test/v1", "ent-model", "", 1, 0)
con.execute(SQL["aiSetProviderSQL"], (1, "platform", "openai", "https://plat.test/v1", "plat-model",
                                      "enc:v1:pk", 1, 0, 500, 700, "USD"))
refresh_digest(1)
check("the platform write switched the base row",
      one("SELECT mode||':'||model FROM ai_provider_configs WHERE tenant_id=1"), "platform:plat-model")
check("the platform write keeps the enterprise snapshot",
      one("SELECT enterprise_model FROM ai_provider_configs WHERE tenant_id=1"), "ent-model")
configure("byo", "openai", "https://ent.test/v1", "ent-model", "", 1, 0)
check("the enterprise write restored the base row",
      one("SELECT mode||':'||model FROM ai_provider_configs WHERE tenant_id=1"), "byo:ent-model")
plat_version = one("SELECT configuration_version FROM ai_provider_configs WHERE tenant_id=1")
set_active("platform")
check("switch to platform copies the platform snapshot",
      one("""SELECT active_service||':'||mode||':'||provider||':'||endpoint||':'||model||':'||
 encrypted_key||':'||enabled||':'||local_only FROM ai_provider_configs WHERE tenant_id=1"""),
      "platform:platform:openai:https://plat.test/v1:plat-model:enc:v1:pk:1:0")
check("switch bumps the version", one("SELECT configuration_version FROM ai_provider_configs WHERE tenant_id=1"),
      plat_version + 1)
check("switch recomputes the digest",
      one("SELECT configuration_digest FROM ai_provider_configs WHERE tenant_id=1"),
      digest_of(plat_version + 1, "platform", "openai", "https://plat.test/v1", "plat-model", SCOPE_HASH))
check("enterprise snapshot survives the switch",
      one("SELECT enterprise_model FROM ai_provider_configs WHERE tenant_id=1"), "ent-model")
set_active("enterprise")
check("switch back restores the enterprise snapshot",
      one("SELECT mode||':'||model||':'||active_service FROM ai_provider_configs WHERE tenant_id=1"),
      "byo:ent-model:enterprise")
check("set provider keeps the platform snapshot for a byo write",
      (con.execute(SQL["aiSetProviderSQL"], (1, "byo", "openai", "https://ent.test/v1", "ent-model",
                                             "", 1, 0, 0, 0, "CNY")), refresh_digest(1),
       one("SELECT platform_provider||':'||platform_model||':'||platform_encrypted_key FROM ai_provider_configs WHERE tenant_id=1"))[2],
      "openai:plat-model:enc:v1:pk")
check("cost currency defaults to CNY",
      one("SELECT cost_currency FROM ai_provider_configs WHERE tenant_id=1"), "CNY")


# ---------------------------------------------------------------- consent
def aicloud_authorized(tenant, cfg_digest, scope):
    sql = (ROOT / "internal/store/queries/platform_ai.sql").read_text()
    stmt = re.search(r"-- name: AICloudAuthorized :one.*?\n\n(.*?);\n", sql, re.S).group(1)
    stmt = stmt.replace("@tenant_id", "?1").replace("@configuration_digest", "?2").replace("@scope_hash", "?3")
    return bool(value(stmt, tenant, cfg_digest, scope))


def consent(allow, cfg_digest="", scope="", uid=1, tenant=1):
    session(tenant, uid)
    err = gate()
    if err:
        return err
    tid, who, _ = actor()
    row = one(SQL["aiConsentConfigSQL"], tid)
    if row is None:
        return "AI_DISABLED"
    provider, enabled, stored_digest, stored_scope = row
    now = 1_700_000_000_000_000
    if allow:
        if provider == "ollama" or not enabled or stored_digest == "" or \
                cfg_digest != stored_digest or scope != stored_scope:
            return "AI_CLOUD_CONSENT_STALE"
        con.execute(SQL["aiConsentUpdateSQL"], (tid, 0, stored_digest, stored_scope, who, now, None))
    else:
        con.execute(SQL["aiConsentUpdateSQL"], (tid, 1, "", "", who, None, now))
    refresh_digest(tid)
    return "ok"


configure("byo", "openai", "https://a.test/v1", "gpt-4", "", 1, 0)
CURRENT = one("SELECT configuration_digest,cloud_scope_hash FROM ai_provider_configs WHERE tenant_id=1")
check("consent refuses an unknown digest", consent(True, "0" * 64, CURRENT[1]), "AI_CLOUD_CONSENT_STALE")
check("consent refuses an unknown scope", consent(True, CURRENT[0], "0" * 64), "AI_CLOUD_CONSENT_STALE")
check("member cannot consent", consent(True, CURRENT[0], CURRENT[1], uid=3), "AI_CONFIGURATION_FORBIDDEN")
check("member cannot revoke either", consent(False, uid=3), "AI_CONFIGURATION_FORBIDDEN")
check("consent granted", consent(True, CURRENT[0], CURRENT[1]), "ok")
check("consent clears local-only",
      one("""SELECT tenant_local_only||':'||cloud_consent_digest||':'||cloud_consent_by||':'||
 (cloud_consent_at IS NOT NULL)||':'||(cloud_consent_revoked_at IS NULL) FROM ai_provider_configs WHERE tenant_id=1"""),
      "0:" + CURRENT[0] + ":1:1:1")
check("cloud export authorized after consent", aicloud_authorized(1, CURRENT[0], CURRENT[1]), True)
check("wrong digest is not authorized", aicloud_authorized(1, "0" * 64, CURRENT[1]), False)
check("identity change invalidates the consent",
      (configure("byo", "openai", "https://a.test/v1", "gpt-5", "", 1, 0),
       aicloud_authorized(1, CURRENT[0], CURRENT[1]))[1], False)
check("the stored consent digest no longer matches the config",
      one("SELECT cloud_consent_digest<>configuration_digest FROM ai_provider_configs WHERE tenant_id=1"), 1)
CURRENT = one("SELECT configuration_digest,cloud_scope_hash FROM ai_provider_configs WHERE tenant_id=1")
check("re-consent with the new digest", consent(True, CURRENT[0], CURRENT[1]), "ok")
check("cloud export authorized again", aicloud_authorized(1, CURRENT[0], CURRENT[1]), True)
check("revoke consent", consent(False), "ok")
check("revoke restores local-only",
      one("""SELECT tenant_local_only||':'||cloud_consent_digest||':'||(cloud_consent_at IS NULL)||':'||
 (cloud_consent_revoked_at IS NOT NULL) FROM ai_provider_configs WHERE tenant_id=1"""), "1::1:1")
check("revoked consent is not authorized", aicloud_authorized(1, CURRENT[0], CURRENT[1]), False)
check("local mode refuses consent",
      (configure("local", "ollama", "http://127.0.0.1:11434", "qwen", "", 1, 1),
       one("SELECT configuration_digest||':'||cloud_scope_hash FROM ai_provider_configs WHERE tenant_id=1"))[1],
      (digest_of(one("SELECT configuration_version FROM ai_provider_configs WHERE tenant_id=1"),
                 "local", "ollama", "http://127.0.0.1:11434", "qwen", SCOPE_HASH) + ":" + SCOPE_HASH))
check("local mode refuses cloud consent",
      consent(True, *one("SELECT configuration_digest,cloud_scope_hash FROM ai_provider_configs WHERE tenant_id=1")),
      "AI_CLOUD_CONSENT_STALE")
check("switching to local revokes a live consent",
      (consent(True, *one("SELECT configuration_digest,cloud_scope_hash FROM ai_provider_configs WHERE tenant_id=1")),
       "AI_CLOUD_CONSENT_STALE")[1], "AI_CLOUD_CONSENT_STALE")
check("a local->byo update does not silently re-grant consent",
      (configure("byo", "openai", "https://a.test/v1", "gpt-4", "", 1, 0),
       one("""SELECT tenant_local_only||':'||(cloud_consent_revoked_at IS NOT NULL)||':'||
 (cloud_consent_at IS NULL) FROM ai_provider_configs WHERE tenant_id=1"""))[1], "1:1:1")
check("cloud export still blocked after the switch",
      aicloud_authorized(1, *one("SELECT configuration_digest,cloud_scope_hash FROM ai_provider_configs WHERE tenant_id=1")),
      False)


# ------------------------------------------------------------- governance
def governance(mode, endpoints, uid=1, tenant=1):
    session(tenant, uid)
    if mode not in ("disabled", "approved", "public"):
        return "AI_GOVERNANCE_INVALID"
    err = gate()
    if err:
        return err
    tid, _, _ = actor()
    encoded = json.dumps(endpoints if endpoints is not None else [], separators=(",", ":"))
    con.execute(SQL["aiGovernanceSQL"], (tid, mode, encoded))
    refresh_digest(tid)
    return "ok"


check("reject an unknown personal mode", governance("global", []), "AI_GOVERNANCE_INVALID")
check("member cannot set the policy", governance("approved", [], uid=3), "AI_CONFIGURATION_FORBIDDEN")
con.execute("DELETE FROM ai_provider_configs WHERE tenant_id=1")
check("governance inserts a fresh policy", governance("approved", ["https://byok.example.test/v1"]), "ok")
check("governance row defaults to a local byo config",
      one("""SELECT mode||':'||provider||':'||enabled||':'||local_only||':'||personal_mode||':'||
 approved_endpoints||':'||tenant_local_only FROM ai_provider_configs WHERE tenant_id=1"""),
      'byo:openai:0:1:approved:["https://byok.example.test/v1"]:1')
check("governance digest covers the inserted identity",
      one("SELECT configuration_digest FROM ai_provider_configs WHERE tenant_id=1"),
      digest_of(1, "byo", "openai", "", "", SCOPE_HASH))
check("nil endpoints become an empty array", governance("public", None), "ok")
check("approved_endpoints serialized as JSON", one("SELECT approved_endpoints FROM ai_provider_configs WHERE tenant_id=1"), "[]")
check("governance keeps the existing identity",
      (governance("disabled", ["https://x.test/v1"]),
       one("SELECT mode||':'||provider||':'||configuration_version FROM ai_provider_configs WHERE tenant_id=1"))[1],
      "byo:openai:1")

print("fails:", failures)
raise SystemExit(1 if failures else 0)
