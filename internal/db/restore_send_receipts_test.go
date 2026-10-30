package db_test

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestNativeBackupRestorePreservesEncryptedSendReceiptsAndHoldsReplay(t *testing.T) {
	for _, tool := range []string{"pg_dump", "pg_restore", "psql"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("native PostgreSQL backup tools unavailable")
		}
	}
	p, _ := testdb.OpenMigrated(t)
	ctx := t.Context()
	box, err := secrets.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	plain := "Message-ID: <backup@fixture>\r\n\r\nprivate retained send body\r\n"
	sealed, err := box.EncryptString(plain)
	if err != nil {
		t.Fatal(err)
	}
	var tenant, domain, mailbox int64
	if err = p.QueryRow(ctx, `WITH b AS(INSERT INTO brands(slug,name)VALUES('send-backup','Backup')RETURNING id),t AS(INSERT INTO tenants(brand_id,name,slug,status)SELECT id,'Backup','send-backup','active' FROM b RETURNING id)SELECT id FROM t`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at)VALUES($1,'send-backup.test','active','proof',now())RETURNING id`, tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash)VALUES($1,$2,'a','a@send-backup.test','fixture')RETURNING id`, tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `INSERT INTO mailbox_send_receipts(tenant_id,mailbox_id,request_key,request_hash,message_id,status,sent_copy_state,raw_secret,smtp_secret,copy_pending_since,accepted_at,lease_until)VALUES
 ($1,$2,'backup-accepted-0001',repeat('a',64),'<backup@fixture>','accepted','pending',$3,$3,now()-interval '8 days',now(),now()+interval '1 hour'),
 ($1,$2,'backup-submitting-0001',repeat('b',64),'<submitting@fixture>','submitting','none',$3,$3,NULL,NULL,now()+interval '1 hour')`, tenant, mailbox, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `INSERT INTO mailbox_scheduled_sends(tenant_id,mailbox_id,draft_folder,draft_uid,draft_uidvalidity,send_at,status,smtp_secret,sent_raw_secret,sent_copy_state,sent_at,copy_pending_since,idempotency_key)
 VALUES($1,$2,'Drafts',42,9,now(),'sent',$3,$3,'unknown',now(),now()-interval '8 days','backup-scheduled-0001')`, tenant, mailbox, sealed); err != nil {
		t.Fatal(err)
	}
	target := testdb.New(t)
	maintenance := nativeRestoreOwned(t, p.Config().ConnConfig.ConnString(), target)
	if err = db.FinalizeRestore(ctx, maintenance, "send-receipt-backup"); err != nil {
		t.Fatal(err)
	}
	quarantineConn, e := pgx.Connect(ctx, maintenance)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = quarantineConn.Exec(ctx, `SELECT urmail_begin_restore_quarantine()`); e != nil {
		t.Fatal(e)
	}
	_ = quarantineConn.Close(ctx)
	if err = db.HoldRestoredSideEffects(ctx, maintenance, "send-receipt-backup"); err != nil {
		t.Fatal(err)
	}
	restored, err := pgx.Connect(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close(context.Background()) }()
	var rawSecret, status, copyState, password string
	var quarantine bool
	if err = restored.QueryRow(ctx, `SELECT raw_secret,status,sent_copy_state,smtp_secret,urmail_restore_quarantine_active() FROM mailbox_send_receipts WHERE request_key='backup-accepted-0001'`).Scan(&rawSecret, &status, &copyState, &password, &quarantine); err != nil {
		t.Fatal(err)
	}
	recovered, err := box.DecryptString(rawSecret)
	if err != nil || recovered != plain || status != "accepted" || copyState != "held" || password != "" || !quarantine {
		t.Fatalf("backup state: decrypt_error=%v content_matches=%v status=%s copy=%s credential_cleared=%v quarantine=%v", err, recovered == plain, status, copyState, password == "", quarantine)
	}
	var safe bool
	if err = restored.QueryRow(ctx, `SELECT status='uncertain' AND smtp_secret='' AND lease_until IS NULL AND raw_secret<>'' FROM mailbox_send_receipts WHERE request_key='backup-submitting-0001'`).Scan(&safe); err != nil || !safe {
		t.Fatal("restored submitting send was not held uncertain")
	}
	if err = restored.QueryRow(ctx, `SELECT sent_copy_state='held' AND smtp_secret='' AND sent_raw IS NULL AND sent_raw_secret=$1 AND copy_pending_since<now()-interval '7 days' FROM mailbox_scheduled_sends WHERE idempotency_key='backup-scheduled-0001'`, sealed).Scan(&safe); err != nil || !safe {
		t.Fatal("restored schedule reset deadline or lost encrypted material")
	}
	var aclSafe bool
	if err = restored.QueryRow(ctx, `SELECT has_table_privilege('urmail_app','mailbox_send_receipts','SELECT') AND NOT has_table_privilege('urmail_app','mailbox_send_receipts','INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') AND NOT has_sequence_privilege('urmail_app','mailbox_send_receipts_id_seq','USAGE,SELECT,UPDATE')`).Scan(&aclSafe); err != nil || !aclSafe {
		t.Fatal("backup/restore reopened write access to send receipts", err)
	}
	if err = db.VerifyRuntimePrivileges(ctx, maintenance); err != nil {
		t.Fatal(err)
	}
}
