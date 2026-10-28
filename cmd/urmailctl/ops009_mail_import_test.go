package main

import (
	"context"
	"testing"
)

func ops009SeedMailImports(t *testing.T, dsn string) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	if _, err := conn.Exec(context.Background(), `SET ROLE urmail_platform`); err != nil {
		t.Fatal(err)
	}
	_, err := conn.Exec(context.Background(), `DO $seed$
 DECLARE tid bigint;mid bigint;preview_job bigint;running_job bigint;completed_job bigint;canceled_job bigint;
 BEGIN
 SELECT id INTO tid FROM tenants WHERE slug='`+ops009TenantSlug+`';
 SELECT id INTO mid FROM mailboxes WHERE tenant_id=tid AND address='alice@`+ops009Domain+`';
 INSERT INTO mailbox_import_jobs(tenant_id,mailbox_id,request_key,format,filename,folder,uid_validity,source_digest,preview_digest,duplicate_policy,source_bytes,status,version)
 VALUES(tid,mid,'ops009-import-preview','eml','preview.eml','INBOX',123,repeat('a',64),repeat('b',64),'skip',128,'preview',3) RETURNING id INTO preview_job;
 INSERT INTO mailbox_import_items(tenant_id,mailbox_id,job_id,item_index,subject,raw_sha256,raw_bytes,raw_secret,selected,status)
 VALUES(tid,mid,preview_job,1,'Unconfirmed source',repeat('c',64),128,'enc:v1:ops009-unconfirmed',false,'ready');
 INSERT INTO mailbox_import_jobs(tenant_id,mailbox_id,request_key,format,filename,folder,uid_validity,source_digest,preview_digest,duplicate_policy,source_bytes,status,version,confirmed_at,lease_token,lease_until)
 VALUES(tid,mid,'ops009-import-running','mbox','running.mbox','INBOX',123,repeat('d',64),repeat('e',64),'keep_both',512,'running',4,now()-interval '2 minutes','ops009-old-import-lease',now()+interval '1 minute') RETURNING id INTO running_job;
 INSERT INTO mailbox_import_items(tenant_id,mailbox_id,job_id,item_index,subject,raw_sha256,raw_bytes,raw_secret,selected,status,baseline_uids,destination_uid,attempts,attempt_started_at,reconciled_at,reconcile_absent)
 VALUES(tid,mid,running_job,1,'Accepted copy',repeat('1',64),128,'',true,'imported',ARRAY[7]::bigint[],42,1,now()-interval '1 minute',NULL,false),
 (tid,mid,running_job,2,'Append in flight',repeat('2',64),128,'enc:v1:ops009-inflight',true,'importing',ARRAY[8]::bigint[],NULL,1,now()-interval '1 minute',NULL,false),
 (tid,mid,running_job,3,'Old absence proof',repeat('3',64),128,'enc:v1:ops009-unknown',true,'unknown',ARRAY[9]::bigint[],NULL,2,now()-interval '1 minute',now(),true),
 (tid,mid,running_job,4,'Confirmed pending copy',repeat('4',64),128,'enc:v1:ops009-pending',true,'pending',ARRAY[10]::bigint[],NULL,0,NULL,NULL,false);
 INSERT INTO mailbox_import_jobs(tenant_id,mailbox_id,request_key,format,filename,folder,uid_validity,source_digest,preview_digest,duplicate_policy,source_bytes,status,confirmed_at)
 VALUES(tid,mid,'ops009-import-completed','eml','completed.eml','INBOX',123,repeat('5',64),repeat('6',64),'skip',128,'completed',now()) RETURNING id INTO completed_job;
 INSERT INTO mailbox_import_items(tenant_id,mailbox_id,job_id,item_index,raw_sha256,raw_bytes,selected,status,destination_uid,attempts)
 VALUES(tid,mid,completed_job,1,repeat('7',64),128,true,'imported',77,1);
 INSERT INTO mailbox_import_jobs(tenant_id,mailbox_id,request_key,format,filename,folder,uid_validity,source_digest,preview_digest,duplicate_policy,source_bytes,status,confirmed_at,cancel_requested)
 VALUES(tid,mid,'ops009-import-canceled','mbox','canceled.mbox','INBOX',123,repeat('8',64),repeat('9',64),'skip',256,'canceled',now(),true) RETURNING id INTO canceled_job;
 INSERT INTO mailbox_import_items(tenant_id,mailbox_id,job_id,item_index,raw_sha256,raw_bytes,raw_secret,selected,status,destination_uid,attempts,reconciled_at,reconcile_absent)
 VALUES(tid,mid,canceled_job,1,repeat('a',64),128,'',true,'imported',88,1,NULL,false),
 (tid,mid,canceled_job,2,repeat('b',64),128,'enc:v1:ops009-canceled-unknown',true,'unknown',NULL,1,now(),true);
 END $seed$`)
	if err != nil {
		t.Fatal(err)
	}
}
func ops009AssertMailImports(t *testing.T, dsn string) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	if _, err := conn.Exec(context.Background(), `SET ROLE urmail_platform`); err != nil {
		t.Fatal(err)
	}
	checks := []struct{ name, sql string }{
		{"unconfirmed import requires a new preview", `SELECT EXISTS(SELECT 1 FROM mailbox_import_jobs j JOIN mailbox_import_items i ON i.job_id=j.id AND i.tenant_id=j.tenant_id AND i.mailbox_id=j.mailbox_id WHERE j.request_key='ops009-import-preview' AND j.status='held' AND j.preview_expires_at<=now() AND j.version>=4 AND j.confirmed_at IS NULL AND i.status='ready' AND NOT i.selected AND i.raw_secret='enc:v1:ops009-unconfirmed')`},
		{"confirmed import lease and old absence proof cannot replay", `SELECT EXISTS(SELECT 1 FROM mailbox_import_jobs j WHERE j.request_key='ops009-import-running' AND j.status='held' AND j.confirmed_at IS NOT NULL AND j.version>=5 AND j.lease_token='' AND j.lease_until IS NULL AND j.source_digest=repeat('d',64) AND j.preview_digest=repeat('e',64)) AND (SELECT count(*)=3 AND bool_and(i.status='unknown' AND i.selected AND i.raw_secret<>'' AND NOT i.reconcile_absent AND i.reconciled_at IS NULL) FROM mailbox_import_items i JOIN mailbox_import_jobs j ON j.tenant_id=i.tenant_id AND j.mailbox_id=i.mailbox_id AND j.id=i.job_id WHERE j.request_key='ops009-import-running' AND i.item_index IN(2,3,4))`},
		{"accepted imported copy and baseline receipts remain facts", `SELECT EXISTS(SELECT 1 FROM mailbox_import_items i JOIN mailbox_import_jobs j ON j.tenant_id=i.tenant_id AND j.mailbox_id=i.mailbox_id AND j.id=i.job_id WHERE j.request_key='ops009-import-running' AND i.item_index=1 AND i.status='imported' AND i.destination_uid=42 AND i.attempts=1 AND i.baseline_uids=ARRAY[7]::bigint[] AND i.raw_secret='')`},
		{"completed and canceled imports cannot resurrect work", `SELECT EXISTS(SELECT 1 FROM mailbox_import_jobs j JOIN mailbox_import_items i ON j.tenant_id=i.tenant_id AND j.mailbox_id=i.mailbox_id AND j.id=i.job_id WHERE j.request_key='ops009-import-completed' AND j.status='completed' AND i.status='imported' AND i.destination_uid=77) AND EXISTS(SELECT 1 FROM mailbox_import_jobs j JOIN mailbox_import_items i ON j.tenant_id=i.tenant_id AND j.mailbox_id=i.mailbox_id AND j.id=i.job_id WHERE j.request_key='ops009-import-canceled' AND j.status='canceled' AND j.cancel_requested AND i.item_index=1 AND i.status='imported' AND i.destination_uid=88) AND EXISTS(SELECT 1 FROM mailbox_import_jobs j JOIN mailbox_import_items i ON j.tenant_id=i.tenant_id AND j.mailbox_id=i.mailbox_id AND j.id=i.job_id WHERE j.request_key='ops009-import-canceled' AND i.item_index=2 AND i.status='unknown' AND i.raw_secret<>'' AND i.reconciled_at IS NULL AND NOT i.reconcile_absent)`},
		{"restore retained import source provenance", `SELECT EXISTS(SELECT 1 FROM audit_logs WHERE action='restore.external_work_held' AND target_type='mailbox_import' AND detail->>'previous_state'='running' AND detail->>'source_digest'=repeat('d',64))`},
	}
	for _, check := range checks {
		var ok bool
		if err := conn.QueryRow(context.Background(), check.sql).Scan(&ok); err != nil || !ok {
			t.Errorf("mail import restore %s: %v %v", check.name, ok, err)
		}
	}
	if _, err := conn.Exec(context.Background(), `SET ROLE urmail_app`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"mailbox_import_jobs", "mailbox_import_items"} {
		var count int64
		if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Errorf("mail import restore RLS %s: %d %v", table, count, err)
		}
	}
}
