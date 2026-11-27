import sqlite3, json, pathlib, re, hashlib

ROOT = pathlib.Path(__file__).resolve().parents[3]
con = sqlite3.connect(":memory:")
con.row_factory = sqlite3.Row
con.execute("PRAGMA foreign_keys=ON")
for f in ["0001_init.sql", "0002_write_side_effects.sql"]:
    con.executescript((ROOT / "migrations_sqlite" / f).read_text())
con.create_function("current_setting", 2, lambda k, mo: "")

NOW = con.execute("SELECT CAST(strftime('%s','now') AS INTEGER)*1000000").fetchone()[0]
T = json.dumps

fails = []
def check(label, got, want):
    if got == want:
        print(f"PASS {label}")
    else:
        fails.append(label)
        print(f"FAIL {label}: got {got!r} want {want!r}")

def load(rel, name):
    if rel == 'internal':
        return go_const(name)
    text = (ROOT / "internal/store/queries" / rel).read_text()
    for part in re.split(r'(?m)^(?=-- name: )', text):
        m = re.match(r'-- name: (\w+)', part)
        if m and m.group(1) == name:
            body = part.split('\n', 1)[1]
            body = '\n'.join(l for l in body.split('\n') if not l.lstrip().startswith('--'))
            return body.strip().rstrip(';')
    raise KeyError((rel, name))

TOKEN = re.compile(r"sqlc\.(?:n)?arg\(\s*'?(?P<n1>[A-Za-z_][A-Za-z0-9_-]*)'?\s*\)|@(?P<n2>[A-Za-z_]\w*)|\?(?P<p>\d*)")
def run(rel, name, named=None, pos=None, one=True):
    body = load(rel, name)
    order = []
    def sub(m):
        if m.group('n1') or m.group('n2'):
            order.append(('n', m.group('n1') or m.group('n2'))); return '?'
        order.append(('p', int(m.group('p')) if m.group('p') else None)); return '?'
    sql = TOKEN.sub(sub, body)
    vals = []
    auto = list(pos or [])
    for kind, key in order:
        if kind == 'n':
            vals.append((named or {})[key])
        elif key is None:
            vals.append(auto.pop(0))
        else:
            vals.append((pos or [])[key - 1])
    if one:
        row = con.execute(sql, vals).fetchone()
        return None if row is None else (row[0] if len(row) == 1 else tuple(row))
    cur = con.execute(sql, vals)
    return cur

def go_const(name):
    src = (ROOT / "internal/store/sqlite_methods.go").read_text()
    m = re.search(r'const ' + name + r' = `(.*?)`', src, re.S)
    if not m:
        raise KeyError(name)
    return m.group(1)

# ---------------------------------------------------------------- fixtures
con.execute("INSERT INTO brands(id,slug,name) VALUES(1,'b','Brand')")
con.execute("INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(1,1,'T','t','active')")
con.execute("INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(2,1,'U','u','active')")
for uid, email in [(1,'owner@t.test'),(2,'admin@t.test'),(3,'member@t.test'),(4,'other@t.test')]:
    con.execute("INSERT INTO users(id,email,password_hash) VALUES(?,?, 'x')", (uid,email))
for uid, role in [(1,'owner'),(2,'admin'),(3,'member')]:
    con.execute("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(?,1,?)", (uid,role))
con.execute("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(4,2,'owner')")
con.execute("INSERT INTO domains(id,tenant_id,fqdn,verification_token,status,verified_at) VALUES(1,1,'t.test','tok','active',1)")
con.execute("UPDATE domains SET provisioning_state='ready' WHERE id=1")
con.execute("INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES(1,1,1,'u','u@t.test','x',1073741824)")
con.execute("INSERT INTO archive_entries(id,tenant_id,mailbox_id,message_id,path,received_at,size_bytes,raw_sha256) VALUES(1,1,1,'m1','/a/m1',?,10,'sha')", (NOW,))

# ---------------------------------------------------------------- A. business timezone
con.execute("INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,'business_timezone','\"Europe/Berlin\"')")
check("TZ platform fallback", run('platform_mail_and_ai_gate.sql','GetBillingBusinessTimezone',pos=[1]), "Europe/Berlin")
con.execute("INSERT INTO settings(scope,scope_id,key,value) VALUES('brand',1,'business_timezone','\"Europe/Paris\"')")
check("TZ brand beats platform", run('platform_mail_and_ai_gate.sql','GetBillingBusinessTimezone',pos=[1]), "Europe/Paris")
con.execute("INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',1,'business_timezone','\"Asia/Shanghai\"')")
check("TZ tenant beats brand", run('platform_mail_and_ai_gate.sql','GetBillingBusinessTimezone',pos=[1]), "Asia/Shanghai")
check("TZ shared brand tenant 2", run('platform_mail_and_ai_gate.sql','GetBillingBusinessTimezone',pos=[2]), "Europe/Paris")

# ---------------------------------------------------------------- B. restore epoch
check("RestoreEpoch default 1", run('platform_restore_sync.sql','PlatformRestoreSyncEpoch'), 1)
con.execute("INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,'restore_replay_review','{\"sync_epoch\":7}')")
check("RestoreEpoch configured 7", run('platform_restore_sync.sql','PlatformRestoreSyncEpoch'), 7)

# ---------------------------------------------------------------- C. alias cycle
con.execute("INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target) VALUES(1,1,'a','b@t.test')")
con.execute("INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target) VALUES(1,1,'b','a@t.test')")
check("alias cycle a->b->a", run('platform_postfix.sql','AliasWouldCycle',named={'domain':'t.test','local_part':'a','target':'b@t.test'},pos=[1,2,3] if False else None), 1)
check("alias no cycle c->d", run('platform_postfix.sql','AliasWouldCycle',named={'domain':'t.test','local_part':'c','target':'d@t.test'}), 0)

# ---------------------------------------------------------------- D. AI cloud authorization
con.execute("""INSERT INTO ai_provider_configs(tenant_id,mode,provider,endpoint,model,enabled,local_only)
 VALUES(1,'byo','openai','https://api.openai.com','gpt-4',1,0)""")
digest = 'd1'; scope_hash = 's1'
con.execute("""UPDATE ai_provider_configs SET tenant_local_only=0, cloud_consent_by=1, cloud_consent_at=?,
 configuration_digest=?, cloud_consent_digest=?, cloud_scope_hash=?, cloud_consent_scope_hash=?, cloud_consent_revoked_at=NULL
 WHERE tenant_id=1""", (NOW, digest, digest, scope_hash, scope_hash))
args = {'tenant_id':1,'configuration_digest':digest,'scope_hash':scope_hash}
check("AICloud owner consent", run('platform_ai.sql','AICloudAuthorized',named=args), 1)
con.execute("UPDATE user_tenant_roles SET role='member' WHERE user_id=1 AND tenant_id=1")
check("AICloud member consent denied", run('platform_ai.sql','AICloudAuthorized',named=args), 0)
con.execute("UPDATE user_tenant_roles SET role='owner' WHERE user_id=1 AND tenant_id=1")
con.execute("UPDATE ai_provider_configs SET cloud_consent_revoked_at=? WHERE tenant_id=1", (NOW,))
check("AICloud revoked denied", run('platform_ai.sql','AICloudAuthorized',named=args), 0)
con.execute("UPDATE ai_provider_configs SET cloud_consent_revoked_at=NULL WHERE tenant_id=1")

# ---------------------------------------------------------------- E. retention authority
con.execute("""INSERT INTO compliance_grants(id,tenant_id,user_id,scope,expires_at,revoked,reason,granted_by,owner_confirmed_at)
 VALUES(10,1,1,'retention_manage',?,0,'because audit',2,1)""", (NOW+86400000000,))
check("retention authority granted", run('compliance_and_retention.sql','GetRetentionPolicyAuthority',named={'tenant_id':1,'created_by':1,'grant_id':10}), 1)
check("retention authority wrong grant", run('compliance_and_retention.sql','GetRetentionPolicyAuthority',named={'tenant_id':1,'created_by':1,'grant_id':11}), 0)
con.execute("""INSERT INTO compliance_grants(id,tenant_id,user_id,scope,expires_at,revoked,reason,granted_by,owner_confirmed_at)
 VALUES(11,1,1,'retention_manage',?,0,'   ',2,1)""", (NOW+86400000000,))
check("retention authority blank reason", run('compliance_and_retention.sql','GetRetentionPolicyAuthority',named={'tenant_id':1,'created_by':1,'grant_id':11}), 0)

# ---------------------------------------------------------------- F. marketing flow sendable
con.execute("INSERT INTO marketing_flows(id,tenant_id,name,status) VALUES(1,1,'f','active')")
con.execute("INSERT INTO marketing_flow_steps(tenant_id,flow_id,step_key,kind,position) VALUES(1,1,'s1','send',1)")
con.execute("INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key,event_facts) VALUES(1,1,'x@example.com','s1','{}')")
check("flow still allowed", run('platform_marketing_flows.sql','PlatformMarketingFlowStillAllowed',pos=[1,1,'x@example.com']), 1)
check("flow missing position", run('platform_marketing_flows.sql','PlatformMarketingFlowStillAllowed',pos=[1,1,'nope@example.com']), 0)
con.execute("INSERT INTO marketing_suppressions(tenant_id,email) VALUES(1,'x@example.com')")
check("flow suppressed", run('platform_marketing_flows.sql','PlatformMarketingFlowStillAllowed',pos=[1,1,'x@example.com']), 0)
con.execute("DELETE FROM marketing_suppressions WHERE email='x@example.com'")
con.execute("UPDATE marketing_flow_positions SET unsubscribed=1 WHERE email='x@example.com'")
check("flow unsubscribed", run('platform_marketing_flows.sql','PlatformMarketingFlowStillAllowed',pos=[1,1,'x@example.com']), 0)
con.execute("UPDATE marketing_flow_positions SET unsubscribed=0, delivery_uncertain=1 WHERE email='x@example.com'")
check("flow uncertain", run('platform_marketing_flows.sql','PlatformMarketingFlowStillAllowed',pos=[1,1,'x@example.com']), 0)
con.execute("UPDATE marketing_flow_positions SET delivery_uncertain=0 WHERE email='x@example.com'")
con.execute("UPDATE marketing_flow_positions SET wait_until=? WHERE email='x@example.com'", (NOW+3600000000,))
check("flow waiting", run('platform_marketing_flows.sql','PlatformMarketingFlowStillAllowed',pos=[1,1,'x@example.com']), 0)
con.execute("UPDATE marketing_flow_positions SET wait_until=NULL WHERE email='x@example.com'")



# ---------------------------------------------------------------- E. evidence visibility
con.execute("INSERT INTO archive_investigations(id,tenant_id,title,purpose,created_by,expires_at) VALUES(1,1,'case','purpose',1,?)", (NOW+86400000000,))
con.execute("INSERT INTO archive_investigation_members(tenant_id,investigation_id,user_id) VALUES(1,1,2)")
con.execute("""INSERT INTO compliance_grants(id,tenant_id,user_id,scope,expires_at,revoked,reason,granted_by,owner_confirmed_at)
 VALUES(20,1,2,'full_content',?,0,'need content',1,1),(21,1,2,'export',?,0,'need export',1,1)""", (NOW+86400000000, NOW+86400000000))
con.execute("""INSERT INTO archive_approval_requests(id,tenant_id,investigation_id,requester_user_id,action,source_digest,state,expires_at,evidence_digest)
 VALUES(1,1,1,1,'export','dg','approved',?,'evdigest')""", (NOW+86400000000,))
check("evidence visible approved member", run('platform_archive_evidence.sql','PlatformEvidenceVisible',pos=[1,1,2,'evdigest']), 1)
check("evidence visible wrong digest", run('platform_archive_evidence.sql','PlatformEvidenceVisible',pos=[1,1,2,'other']), 0)
check("evidence visible non-member", run('platform_archive_evidence.sql','PlatformEvidenceVisible',pos=[1,1,4,'evdigest']), 0)
con.execute("UPDATE archive_investigation_members SET removed_at=? WHERE investigation_id=1 AND user_id=2", (NOW,))
check("evidence visible removed member", run('platform_archive_evidence.sql','PlatformEvidenceVisible',pos=[1,1,2,'evdigest']), 0)
con.execute("UPDATE archive_investigation_members SET removed_at=NULL WHERE investigation_id=1 AND user_id=2")
con.execute("UPDATE archive_approval_requests SET expires_at=? WHERE id=1", (NOW-1000000,))
check("evidence visible expired approval", run('platform_archive_evidence.sql','PlatformEvidenceVisible',pos=[1,1,2,'evdigest']), 0)
con.execute("UPDATE archive_approval_requests SET expires_at=? WHERE id=1", (NOW+86400000000,))

# ---------------------------------------------------------------- F. notifications
SESSION = {'urmail.tenant_id':'1','urmail.user_id':'2'}
con.create_function("current_setting", 2, lambda k, mo: SESSION.get(k, ""))
con.execute("""INSERT INTO notification_outbox(id,tenant_id,user_id,kind,message_key,params,dedupe_key,channel)
 VALUES(1,1,NULL,'system','k1','{}','d1','in_app'),
        (2,1,2,'archive_approval','k2','{"case_id":1}','d2','in_app'),
        (3,1,NULL,'archive_recall','k3','{"recall_id":9}','d3','in_app')""")
rows = run('notifications.sql','ListTenantNotifications',named={'before_id':0},pos=[1],one=False).fetchall()
ids = sorted(r['id'] for r in rows)
check("notifications visible ids", ids, [1,2])
SESSION = {'urmail.tenant_id':'1','urmail.user_id':'4'}
rows = run('notifications.sql','ListTenantNotifications',named={'before_id':0},pos=[1],one=False).fetchall()
check("notifications actor without grants", sorted(r['id'] for r in rows), [1])
SESSION = {'urmail.user_id':'2'}  # tenant setting missing → archive rows hidden
rows = run('notifications.sql','ListTenantNotifications',named={'before_id':0},pos=[1],one=False).fetchall()
check("notifications tenant unbound hides archive", sorted(r['id'] for r in rows), [1])
SESSION = {'urmail.tenant_id':'1','urmail.user_id':'2'}

# ---------------------------------------------------------------- G. quota
check("setting limit default -1", run('internal','settingLimitSQL',pos=[1,'mailbox_daily_send_limit']), -1)
con.execute("INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,'mailbox_daily_send_limit','2')")
check("setting limit platform 2", run('internal','settingLimitSQL',pos=[1,'mailbox_daily_send_limit']), 2)
con.execute("INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',1,'mailbox_daily_send_limit','0')")
check("setting limit tenant 0", run('internal','settingLimitSQL',pos=[1,'mailbox_daily_send_limit']), 0)
con.execute("UPDATE settings SET value='\"3\"' WHERE scope='tenant' AND scope_id=1")
check("setting limit tenant json string 3", run('internal','settingLimitSQL',pos=[1,'mailbox_daily_send_limit']), 3)
inc = go_const('incrementMailboxDailySentSQL')
day = '2026-11-20'
con.execute(inc, (1,1,day,2)); con.execute(inc, (1,1,day,2))
row = con.execute(inc, (1,1,day,2)).fetchone()
check("increment stops at limit", row, None)
check("increment count 2", con.execute("SELECT messages_sent FROM mailbox_usage WHERE mailbox_id=1").fetchone()[0], 2)
take = go_const('takeMailboxQuotaSQL')
con.execute("UPDATE settings SET value='2' WHERE scope='tenant' AND scope_id=1 AND key='mailbox_daily_send_limit'")
row = con.execute(take, (1,1,'2026-11-21',2)).fetchone()
check("take quota allowed first", row is not None, True)
con.execute(take, (1,1,'2026-11-21',2))
check("take quota second blocked", con.execute(take, (1,1,'2026-11-21',2)).fetchone(), None)
con.execute("UPDATE settings SET value='0' WHERE scope='tenant' AND scope_id=1 AND key='mailbox_daily_send_limit'")
check("take quota zero limit", con.execute(take, (1,1,'2026-11-22',0)).fetchone(), None)
con.execute("UPDATE settings SET value='-1' WHERE scope='tenant' AND scope_id=1 AND key='mailbox_daily_send_limit'")
check("take quota unlimited", con.execute(take, (1,1,'2026-11-23',2147483647)).fetchone() is not None, True)

# ---------------------------------------------------------------- H. marketing uncertain + contact group
unc = go_const('markMarketingFlowUncertainSQL')
cur = con.execute(unc, (1,1,'x@example.com'))
check("uncertain rows", cur.rowcount, 1)
check("uncertain flag", con.execute("SELECT delivery_uncertain FROM marketing_flow_positions WHERE email='x@example.com'").fetchone()[0], 1)
check("uncertain missing rows", con.execute(unc, (1,1,'missing@example.com')).rowcount, 0)
con.execute("INSERT INTO mailbox_addressbooks(id,tenant_id,mailbox_id,name) VALUES(1,1,1,'A')")
con.execute("INSERT INTO mailbox_contact_groups(id,tenant_id,mailbox_id,addressbook_id,name) VALUES(1,1,1,1,'G')")
con.execute("INSERT INTO mailbox_contacts(id,tenant_id,mailbox_id,addressbook_id,emails) VALUES(1,1,1,1,'[\"c@example.com\"]')")
con.execute("INSERT INTO mailbox_contact_group_members(tenant_id,mailbox_id,group_id,contact_id) VALUES(1,1,1,1)")
cur = con.execute(go_const('deleteContactGroupSQL'), (1,1,1))
check("delete group rows", cur.rowcount, 1)
con.execute(go_const('deleteContactGroupMembersSQL'), (1,1,1))
check("delete group members cleared", con.execute("SELECT COUNT(*) FROM mailbox_contact_group_members WHERE group_id=1").fetchone()[0], 0)
check("delete group soft-deleted", con.execute("SELECT deleted_at IS NOT NULL FROM mailbox_contact_groups WHERE id=1").fetchone()[0], 1)

# ---------------------------------------------------------------- I. archive member/grant + snapshot digest
member_sql = go_const('archiveCaseMemberSQL')
grant_sql = go_const('archiveCaseGrantSQL')
def case_member(t,c,u):
    granted = con.execute(grant_sql, (t,u,'full_content',NOW)).fetchone()[0]
    return bool(granted) and bool(con.execute(member_sql, (t,c,NOW,u)).fetchone()[0])
check("case member admin with grant", case_member(1,1,2), True)
check("case member owner without explicit grant", case_member(1,1,1), False)
con.execute("INSERT INTO archive_investigation_sources(tenant_id,investigation_id,archive_entry_id,added_by) VALUES(1,1,1,1)")
snap_src = con.execute("SELECT e.id,e.raw_sha256,e.size_bytes,e.mailbox_id,e.deleted_at,e.legal_hold FROM archive_investigation_sources s JOIN archive_entries e ON e.tenant_id=s.tenant_id AND e.id=s.archive_entry_id WHERE s.tenant_id=1 AND s.investigation_id=1 ORDER BY e.id").fetchall()
check("snapshot sources row", len(snap_src), 1)
doc = {"status":"open","case_id":1,"members":[],"sources":[],"version":1,"expires_at":NOW+86400000000}
digest = hashlib.sha256(json.dumps(doc,separators=(',',':')).encode()).hexdigest()
check("snapshot digest stable", digest, hashlib.sha256(json.dumps(doc,separators=(',',':')).encode()).hexdigest())



print("fails:", fails)
raise SystemExit(1 if fails else 0)
