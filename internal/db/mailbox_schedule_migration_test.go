package db_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestMailboxSchedule76UpgradeAndDowngradeNeverReplay(t *testing.T) {
	dsn := testdb.New(t)
	target, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := target.Query()
	params.Set("role", "urmail_migrate")
	target.RawQuery = params.Encode()
	m, err := db.NewMigrator(target.String())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	if err = m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err = m.Down(ctx); err != nil {
		t.Fatal(err)
	}
	version, dirty, err := m.Version()
	if err != nil || dirty || version != 76 {
		t.Fatalf("version=%d dirty=%v err=%v", version, dirty, err)
	}
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
	if err = m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var ok bool
	err = conn.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(status='held' AND smtp_secret='' AND draft_uidvalidity=0 AND last_error='SCHEDULE_REVIEW_REQUIRED') FROM mailbox_scheduled_sends WHERE draft_uid<=2`).Scan(&ok)
	if err != nil || !ok {
		t.Fatalf("legacy upgrade replay guard=%v err=%v", ok, err)
	}
	if err = m.Down(ctx); err != nil {
		t.Fatal(err)
	}
	err = conn.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(status='failed' AND smtp_secret='') FROM mailbox_scheduled_sends WHERE draft_uid<=2`).Scan(&ok)
	if err != nil || !ok {
		t.Fatalf("downgrade replay guard=%v err=%v", ok, err)
	}
}
