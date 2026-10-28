package main

import (
	"context"
	"testing"
)

// New execution proofs are seeded before the same real archive/restore journey,
// rather than tested only against an independently invoked worker.
func ops009SeedRecallTransport(t *testing.T, dsn string) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	if _, err := conn.Exec(context.Background(), `SET ROLE urmail_platform`); err != nil {
		t.Fatal(err)
	}
	_, err := conn.Exec(context.Background(), `DO $seed$
 DECLARE tid bigint;did bigint;mid bigint;actor bigint;source bigint;pending_source bigint;proof bytea;token text;copy_guid text:=repeat('a',32);copy_key text:=repeat('a',32)||':123:9';
 BEGIN
 SELECT id INTO tid FROM tenants WHERE slug='`+ops009TenantSlug+`';
 SELECT id INTO did FROM domains WHERE tenant_id=tid AND fqdn='`+ops009Domain+`';
 SELECT id INTO mid FROM mailboxes WHERE tenant_id=tid AND address='alice@`+ops009Domain+`';
 SELECT id INTO actor FROM users WHERE email='ops009-owner@example.test';
 SELECT id INTO source FROM archive_entries WHERE tenant_id=tid AND message_id='<ops009-archive-1@drill>';
 UPDATE brand_hosts SET status='active',cert_issued_at=now(),activated_at=now() WHERE hostname='`+ops009BrandHost+`';
 INSERT INTO sessions(id,user_id,tenant_id,csrf_token,expires_at,origin_host) VALUES('ops009-brandhost-session',actor,tid,'fixture-csrf',now()+interval '1 day','`+ops009BrandHost+`');
 INSERT INTO mailbox_sessions(token_hash,tenant_id,mailbox_id,password_version,expires_at,origin_host) SELECT repeat('9',64),tid,mid,md5(password_hash),now()+interval '1 day','`+ops009BrandHost+`' FROM mailboxes WHERE tenant_id=tid AND id=mid;
 PERFORM set_config('urmail.tenant_id',tid::text,true);PERFORM set_config('urmail.user_id',actor::text,true);
 proof:=convert_to(jsonb_build_object('tenant_id',tid,'actor_id',actor,'entry_id',source,'mailbox_id',mid,'address','alice@`+ops009Domain+`','message_id','<ops009-archive-1@drill>','copies',jsonb_build_array(jsonb_build_object('guid',copy_guid,'uid_validity',123,'uid',9,'message_guid','1760000000.M1P42.preview')) )::text,'UTF8');
 INSERT INTO recall_requests(tenant_id,archive_entry_id,mailbox_id,message_id,requested_by,status,reason,preview_actor_id,preview_token,preview_digest,preview_snapshot,preview_expires_at,attempt_epoch)
 VALUES(tid,source,mid,'<ops009-archive-1@drill>',actor,'preview','ops009 unconfirmed recall',actor,'00000000-0000-4000-8000-000000000001',encode(sha256(proof),'hex'),proof,statement_timestamp()+interval '5 minutes',1);
 INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,received_at) VALUES(tid,mid,'<ops009-recall-pending@drill>','cur/ops009-recall-pending',now()) RETURNING id INTO pending_source;
 proof:=convert_to(jsonb_build_object('tenant_id',tid,'actor_id',actor,'entry_id',pending_source,'mailbox_id',mid,'address','alice@`+ops009Domain+`','message_id','<ops009-recall-pending@drill>','copies',jsonb_build_array(jsonb_build_object('guid',copy_guid,'uid_validity',123,'uid',9,'message_guid','1760000000.M1P42.pending')) )::text,'UTF8');
 INSERT INTO recall_requests(tenant_id,archive_entry_id,mailbox_id,message_id,requested_by,status,reason,preview_actor_id,preview_token,preview_digest,preview_snapshot,preview_expires_at,attempt_epoch)
 VALUES(tid,pending_source,mid,'<ops009-recall-pending@drill>',actor,'preview','ops009 confirmed recall',actor,'00000000-0000-4000-8000-000000000002',encode(sha256(proof),'hex'),proof,statement_timestamp()+interval '5 minutes',1);
 UPDATE recall_requests SET status='pending',confirmed_at=now(),selected_copy_keys=jsonb_build_array(copy_key),local_result='{"items":[{"key":"retained-success","status":"success"},{"key":"retained-unknown","status":"uncertain"}]}'::jsonb,result_history='[{"attempt":1,"status":"partial"}]'::jsonb WHERE tenant_id=tid AND archive_entry_id=pending_source;
 INSERT INTO mailbox_scheduled_sends(tenant_id,mailbox_id,draft_folder,draft_uid,draft_uidvalidity,send_at,status,smtp_secret,sent_at,sent_raw,sent_copy_state,idempotency_key)
 VALUES(tid,mid,'Drafts',987,123,now(),'sent','fixture-obsolete-password',now(),convert_to('Message-ID: <ops009-accepted-copy@drill>'||chr(13)||chr(10)||chr(13)||chr(10)||'retained accepted raw','UTF8'),'unknown','ops009-accepted-copy');
 INSERT INTO domain_dns_security(tenant_id,domain_id,dnssec_enabled,secondary_hostname,secondary_address,primary_address,tsig_key_name) VALUES(tid,did,true,'ns2.ops009.example.test','192.0.2.20','192.0.2.10','ops009-transfer');
 UPDATE domain_transport_security SET enabled=true,policy_mx=ARRAY['mx.ops009.example.test'],max_age=86400,published_at=now()-interval '3 days',testing_since=now()-interval '4 days',cache_safe_after=now()-interval '2 days',observation_digest=repeat('c',64),observed_at=now(),observation_status='verified',observation_detail='{"dns_id":true,"mx":true,"https_policy":true,"smtp_tls":true}'::jsonb WHERE tenant_id=tid AND domain_id=did;
 INSERT INTO domain_tls_reports(tenant_id,domain_id,report_digest,reporter_digest,report_id_digest,period_start,period_end,successful,failed,failures) VALUES(tid,did,repeat('d',64),repeat('e',64),repeat('f',64),now()-interval '1 day',now(),20,2,'{"certificate-expired":2}'::jsonb);
 END $seed$`)
	if err != nil {
		t.Fatal(err)
	}
}
func ops009AssertRecallTransport(t *testing.T, dsn string) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	if _, err := conn.Exec(context.Background(), `SET ROLE urmail_platform`); err != nil {
		t.Fatal(err)
	}
	checks := []struct{ name, query string }{
		{"origin-bound panel and mailbox sessions cannot survive restore", `SELECT EXISTS(SELECT 1 FROM sessions WHERE id='ops009-brandhost-session' AND expires_at<=now() AND origin_host='` + ops009BrandHost + `') AND EXISTS(SELECT 1 FROM mailbox_sessions WHERE token_hash=repeat('9',64) AND expires_at<=now() AND revoked_at IS NOT NULL AND origin_host='` + ops009BrandHost + `')`},
		{"accepted Sent payload held for explicit fresh-owner repair", `SELECT EXISTS(SELECT 1 FROM mailbox_scheduled_sends s JOIN tenants t ON t.id=s.tenant_id WHERE t.slug='` + ops009TenantSlug + `' AND s.idempotency_key='ops009-accepted-copy' AND s.status='sent' AND s.sent_at IS NOT NULL AND s.sent_raw=convert_to('Message-ID: <ops009-accepted-copy@drill>'||chr(13)||chr(10)||chr(13)||chr(10)||'retained accepted raw','UTF8') AND s.sent_copy_state='held' AND s.smtp_secret='' AND s.lease_until IS NULL AND s.last_error='RESTORE_RECONCILIATION_REQUIRED')`},
		{"unconfirmed recall expired without losing original proof", `SELECT EXISTS(SELECT 1 FROM recall_requests r JOIN tenants t ON t.id=r.tenant_id WHERE t.slug='` + ops009TenantSlug + `' AND r.message_id='<ops009-archive-1@drill>' AND r.status='expired' AND r.preview_expires_at<=now() AND r.preview_snapshot IS NOT NULL AND encode(sha256(r.preview_snapshot),'hex')=r.preview_digest AND position('RESTORE_RECONCILIATION_REQUIRED' in r.reason)>0)`},
		{"confirmed recall intent and partial results preserved under hold", `SELECT EXISTS(SELECT 1 FROM recall_requests r JOIN tenants t ON t.id=r.tenant_id WHERE t.slug='` + ops009TenantSlug + `' AND r.message_id='<ops009-recall-pending@drill>' AND r.status='uncertain' AND r.confirmed_at IS NOT NULL AND r.preview_snapshot IS NOT NULL AND r.selected_copy_keys<>'[]'::jsonb AND r.local_result->'items'->0->>'status'='success' AND r.local_result->'items'->1->>'status'='uncertain' AND jsonb_array_length(r.result_history)=1 AND position('RESTORE_RECONCILIATION_REQUIRED' in r.reason)>0)`},
		{"published enforcement preserved but stale observation authority removed", `SELECT EXISTS(SELECT 1 FROM domain_transport_security s JOIN tenants t ON t.id=s.tenant_id WHERE t.slug='` + ops009TenantSlug + `' AND s.enabled AND s.mtasts_mode='enforce' AND s.policy_mx=ARRAY['mx.ops009.example.test'] AND s.published_at IS NOT NULL AND s.observed_at IS NULL AND s.observation_digest IS NULL AND s.observation_status='unverified' AND s.observation_detail='{}'::jsonb)`},
		{"TLS report evidence is preserved without inventing reporter verification", `SELECT EXISTS(SELECT 1 FROM domain_tls_reports r JOIN tenants t ON t.id=r.tenant_id WHERE t.slug='` + ops009TenantSlug + `' AND r.report_digest=repeat('d',64) AND r.successful=20 AND r.failed=2 AND r.failures='{"certificate-expired":2}'::jsonb AND NOT r.source_verified)`},
		{"DNSSEC and secondary intent metadata is retained", `SELECT EXISTS(SELECT 1 FROM domain_dns_security s JOIN tenants t ON t.id=s.tenant_id WHERE t.slug='` + ops009TenantSlug + `' AND s.dnssec_enabled AND s.secondary_hostname='ns2.ops009.example.test' AND s.tsig_key_name='ops009-transfer')`},
	}
	for _, check := range checks {
		var ok bool
		if err := conn.QueryRow(context.Background(), check.query).Scan(&ok); err != nil || !ok {
			t.Errorf("latest restore fact %s: %v %v", check.name, ok, err)
		}
	}
	// Platform intent is not proof that restored filesystem keys match the parent
	// DS. No private key or manufactured public delegation result is seeded here.
	if _, err := conn.Exec(context.Background(), `SET ROLE urmail_app`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"domain_dns_security", "domain_transport_security", "domain_tls_reports"} {
		var count int64
		err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count)
		if err != nil || count != 0 {
			t.Errorf("restored %s RLS failed closed: %d %v", table, count, err)
		}
	}
}
