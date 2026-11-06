package db_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/db"
)

func v3Seed(t *testing.T) (*db.Pool, *db.Pool, int64, int64) {
	t.Helper()
	url := testDatabaseURL(t)
	applyMigrations(t, url)
	ctx := context.Background()
	platform, err := db.OpenPlatform(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(platform.Close)
	app, err := db.OpenApp(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	truncate(t, platform)
	var brand, tenant, domain int64
	if err = platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('v3','V3') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'V3','v3','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,verification_token,status,verified_at,provisioning_state) VALUES($1,'v3.example.test','x','active',now(),'ready') RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	return platform, app, tenant, domain
}
func v3Code(t *testing.T, err error, code string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || !strings.Contains(pg.Message, code) {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestV3SettingsDeleteAndTenantReferences(t *testing.T) {
	p, a, tid, did := v3Seed(t)
	ctx := context.Background()
	if _, err := p.Exec(ctx, `INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,'theme','{}'),('tenant',$1,'theme','{}')`, tid); err != nil {
		t.Fatal(err)
	}
	tx, err := a.TenantTx(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := tx.Exec(ctx, "DELETE FROM settings WHERE scope='platform'")
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("platform delete: %v %v", tag, err)
	}
	tag, err = tx.Exec(ctx, "DELETE FROM settings WHERE scope='tenant' AND scope_id=$1", tid)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("own delete: %v %v", tag, err)
	}
	_ = tx.Rollback(ctx)
	var other, mailbox int64
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) SELECT brand_id,'Other','other','active' FROM tenants WHERE id=$1 RETURNING id", tid).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash) VALUES($1,$2,'a','a@v3.example.test','x') RETURNING id", tid, did).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	tx, err = a.TenantTx(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,received_at) VALUES($1,$2,'cross','/not-read',now())", other, mailbox)
	if err == nil {
		err = tx.Commit(ctx)
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23503" {
		t.Fatalf("cross reference accepted: %v", err)
	}
}

func TestV3HoldProtectsSoftDeleteAndCascades(t *testing.T) {
	p, _, tid, did := v3Seed(t)
	ctx := context.Background()
	var mailbox int64
	if err := p.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash) VALUES($1,$2,'held','held@v3.example.test','x') RETURNING id", tid, did).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,received_at,legal_hold) VALUES($1,$2,'held','/not-read',now(),true)", tid, mailbox); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"DELETE FROM domains WHERE tenant_id=$1", "DELETE FROM mailboxes WHERE tenant_id=$1", "UPDATE mailboxes SET status='deleted' WHERE tenant_id=$1", "DELETE FROM tenants WHERE id=$1"} {
		_, err := p.Exec(ctx, query, tid)
		v3Code(t, err, "LEGAL_HOLD")
	}
	if _, err := p.Exec(ctx, "UPDATE archive_entries SET legal_hold=false WHERE tenant_id=$1", tid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "UPDATE mailboxes SET status='deleted' WHERE tenant_id=$1", tid); err != nil {
		t.Fatal(err)
	}
	var still int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM mailboxes WHERE id=$1", mailbox).Scan(&still); err != nil || still != 1 {
		t.Fatalf("retained mailbox disappeared: %d %v", still, err)
	}
}

func TestV3AllocationConcurrencyAndPastDue(t *testing.T) {
	p, _, tid, did := v3Seed(t)
	ctx := context.Background()
	if _, err := p.Exec(ctx, "INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',$1,'mailbox_limit','1')", tid); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 20; round++ {
		var successful atomic.Int32
		var group sync.WaitGroup
		failures := make(chan error, 20)
		for i := 0; i < 20; i++ {
			group.Add(1)
			go func(i int) {
				defer group.Done()
				_, err := p.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,$3,$4,'x',1024)", tid, did, fmt.Sprintf("r%dm%d", round, i), fmt.Sprintf("r%dm%d@v3.example.test", round, i))
				if err == nil {
					successful.Add(1)
					return
				}
				var pg *pgconn.PgError
				if !errors.As(err, &pg) || pg.Message != "MAILBOX_LIMIT_EXCEEDED" {
					failures <- err
				}
			}(i)
		}
		group.Wait()
		close(failures)
		for err := range failures {
			t.Errorf("unexpected concurrent error: %v", err)
		}
		if successful.Load() != 1 {
			t.Fatalf("round %d successes=%d", round, successful.Load())
		}
		if _, err := p.Exec(ctx, "UPDATE mailboxes SET status='deleted' WHERE tenant_id=$1", tid); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, "DELETE FROM mailboxes WHERE tenant_id=$1", tid); err != nil {
			t.Fatal(err)
		}
	}
}

func TestV3BillingAndRecallModels(t *testing.T) {
	p, _, tid, _ := v3Seed(t)
	ctx := context.Background()
	var user int64
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('model@example.test','x') RETURNING id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	var recall int64
	if err := p.QueryRow(ctx, "INSERT INTO recall_requests(tenant_id,message_id,requested_by) VALUES($1,'remote', $2) RETURNING id", tid, user).Scan(&recall); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "UPDATE recall_requests SET status='delivered' WHERE tenant_id=$1 AND id=$2", tid, recall); err == nil {
		t.Fatal("unconfirmed remote recall marked delivered")
	}
	if _, err := p.Exec(ctx, "UPDATE recall_requests SET status='failed',reason='remote_delivery_not_recallable' WHERE tenant_id=$1 AND id=$2", tid, recall); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "UPDATE recall_requests SET status='pending' WHERE tenant_id=$1 AND id=$2", tid, recall); err == nil {
		t.Fatal("terminal recall reopened")
	}
}

func TestV3MailboxStorageIdentityCannotEscape(t *testing.T) {
	p, _, tid, did := v3Seed(t)
	ctx := context.Background()
	for _, local := range []string{"..", "../other/user", "/absolute"} {
		if _, err := p.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash) VALUES($1,$2,$3,$4,'x')", tid, did, local, local+"@v3.example.test"); err == nil {
			t.Fatalf("unsafe local part accepted: %q", local)
		}
	}
	if _, err := p.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash) VALUES($1,$2,'safe','../escape@v3.example.test','x')", tid, did); err == nil {
		t.Fatal("address/local-part mismatch accepted")
	}
}

func TestV3StorageReservationSurvivesMailboxDelete(t *testing.T) {
	p, _, tid, did := v3Seed(t)
	ctx := context.Background()
	var id int64
	if err := p.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash) VALUES($1,$2,'reserved','reserved@v3.example.test','x') RETURNING id", tid, did).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "UPDATE mailboxes SET status='deleted' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "DELETE FROM mailboxes WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	_, err := p.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash) VALUES($1,$2,'reserved','reserved@v3.example.test','x')", tid, did)
	v3Code(t, err, "MAILBOX_STORAGE_ADDRESS_RESERVED")
}

func TestV3RecallLocalCompletionRequiresPlatform(t *testing.T) {
	p, a, tid, did := v3Seed(t)
	ctx := context.Background()
	var uid, mid, aid, rid int64
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('recall@v3.test','x') RETURNING id").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash) VALUES($1,$2,'recall','recall@v3.example.test','x') RETURNING id", tid, did).Scan(&mid); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,received_at) VALUES($1,$2,'recall-proof','/archive',now()) RETURNING id", tid, mid).Scan(&aid); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO recall_requests(tenant_id,archive_entry_id,mailbox_id,message_id,requested_by) VALUES($1,$2,$3,'recall-proof',$4) RETURNING id", tid, aid, mid, uid).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	tx, err := a.TenantTx(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "UPDATE recall_requests SET status='local_recalled',completed_at=now() WHERE tenant_id=$1 AND id=$2", tid, rid)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != "RECALL_STATE_TRANSITION_REJECTED" {
		t.Fatalf("app forged local completion: %v", err)
	}
	_ = tx.Rollback(ctx)
	// Platform authority alone cannot replace a reviewed confirmation either.
	_, err = p.Exec(ctx, "UPDATE recall_requests SET status='local_recalled',completed_at=now() WHERE tenant_id=$1 AND id=$2", tid, rid)
	if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != "RECALL_VERIFIED_LOCAL_PROOF_REQUIRED" {
		t.Fatalf("platform completed an unreviewed recall: %v", err)
	}
}
