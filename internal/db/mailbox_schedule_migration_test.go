package db_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// TestMailboxSchedule76UpgradeAndDowngradeNeverReplay pins migration 0077's
// delivery-uncertainty guards: a legacy 0076 schedule row must never turn
// back into runnable work, in either direction. The versions are explicit so
// later migrations cannot change the schema this fixture seeds against.
func TestMailboxSchedule76UpgradeAndDowngradeNeverReplay(t *testing.T) {
	dsn := testdb.New(t)
	target, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := target.Query()
	params.Set("role", "urmail_migrate")
	target.RawQuery = params.Encode()
	if _, err := db.NewMigrator(target.String()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Seed the legacy shape: apply everything up to and including 0076.
	testdb.MigrateTo(t, dsn, 76)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `SET ROLE urmail_platform;
 DO $$ DECLARE b bigint;t bigint;d bigint;m bigint; BEGIN
 INSERT INTO brands(slug,name)VALUES('schedule-upgrade','Schedule upgrade')RETURNING id INTO b;
 INSERT INTO tenants(brand_id,name,slug,status)VALUES(b,'Schedule','schedule-upgrade','active')RETURNING id INTO t;
 INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at)VALUES(t,'schedule.example.test','active','fixture',now())RETURNING id INTO d;
 INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes)VALUES(t,d,'alice','alice@schedule.example.test','fixture',1024)RETURNING id INTO m;
 INSERT INTO mailbox_scheduled_sends(tenant_id,mailbox_id,draft_folder,draft_uid,send_at,status,smtp_secret)
 SELECT t,m,'Drafts',n,now()-interval '1 hour',state,'enc:legacy' FROM(VALUES(1,'pending'),(1,'sending'),(3,'sent'))v(n,state);
 END $$;`)
	if err != nil {
		t.Fatal(err)
	}
	// Apply 0077 (and anything stacked on top of it): the legacy pending and
	// sending rows must be parked as review-required evidence.
	testdb.MigrateTo(t, dsn, 78)
	var ok bool
	err = conn.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(status='held' AND smtp_secret='' AND draft_uidvalidity=0 AND last_error='SCHEDULE_REVIEW_REQUIRED') FROM mailbox_scheduled_sends WHERE draft_uid<=2`).Scan(&ok)
	if err != nil || !ok {
		t.Fatalf("legacy upgrade replay guard=%v err=%v", ok, err)
	}
	// Stepping back to 0076 must downgrade the parked rows to failed instead
	// of handing the SMTP secret back to a runnable worker.
	testdb.MigrateTo(t, dsn, 76)
	err = conn.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(status='failed' AND smtp_secret='') FROM mailbox_scheduled_sends WHERE draft_uid<=2`).Scan(&ok)
	if err != nil || !ok {
		t.Fatalf("downgrade replay guard=%v err=%v", ok, err)
	}
}
