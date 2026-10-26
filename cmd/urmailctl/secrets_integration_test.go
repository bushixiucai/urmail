package main

import (
	"context"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestLegacyTOTPMigrationIsEncryptedAuditedAndIdempotent(t *testing.T) {
	dsn := testdb.New(t)
	testdb.MigrateTo(t, dsn, 80)
	ctx := context.Background()
	pool, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	seed := "JBSWY3DPEHPK3PXP"
	var id int64
	if err := pool.QueryRow(ctx, "INSERT INTO users(email,password_hash,mfa_secret) VALUES('legacy-totp@example.test','fixture-hash',$1) RETURNING id", seed).Scan(&id); err != nil {
		t.Fatal(err)
	}
	migrator, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrator.Close() }()
	if err := migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := encryptLegacyTOTPBatch(ctx, pool, box, 10); err != nil || n != 1 {
		t.Fatalf("migration count=%d err=%v", n, err)
	}
	var stored string
	if err := pool.QueryRow(ctx, "SELECT mfa_secret FROM users WHERE id=$1", id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "enc:v1:") {
		t.Fatal("migrated seed is not encrypted")
	}
	if plain, err := box.DecryptString(stored); err != nil || plain != seed {
		t.Fatal("migrated seed cannot be restored with its original key")
	}
	if n, err := encryptLegacyTOTPBatch(ctx, pool, box, 10); err != nil || n != 0 {
		t.Fatalf("idempotent migration count=%d err=%v", n, err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action='MAINTENANCE secrets.totp_migrated'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET mfa_secret=$2 WHERE id=$1", id, seed); err == nil {
		t.Fatal("plaintext seed write accepted after migration")
	}
}

func TestLegacyTOTPMigrationRollsBackBadSeedAndAuditFailure(t *testing.T) {
	dsn := testdb.New(t)
	testdb.MigrateTo(t, dsn, 80)
	ctx := context.Background()
	pool, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, fixture := range []struct{ email, seed string }{
		{"first-seed@example.test", "JBSWY3DPEHPK3PXP"},
		{"second-seed@example.test", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"},
		{"bad-seed@example.test", "invalid-legacy-seed"},
	} {
		if _, err := pool.Exec(ctx, "INSERT INTO users(email,password_hash,mfa_secret) VALUES($1,'fixture-hash',$2)", fixture.email, fixture.seed); err != nil {
			t.Fatal(err)
		}
	}
	migrator, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrator.Close() }()
	if err := migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	assertUnchanged := func() {
		t.Helper()
		var encrypted, audits int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE mfa_secret LIKE 'enc:v1:%'").Scan(&encrypted); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action='MAINTENANCE secrets.totp_migrated'").Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if encrypted != 0 || audits != 0 {
			t.Fatalf("failed batch committed seeds=%d audits=%d", encrypted, audits)
		}
	}
	if _, err := encryptLegacyTOTPBatch(ctx, pool, box, 10); err == nil {
		t.Fatal("mixed batch accepted an invalid seed")
	}
	assertUnchanged()
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE email='bad-seed@example.test'"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_second_seed_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.action='MAINTENANCE secrets.totp_migrated' AND NEW.target_id=(SELECT id::text FROM users WHERE email='second-seed@example.test') THEN
  RAISE EXCEPTION 'isolated audit failure';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER reject_second_seed_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_second_seed_audit();`); err != nil {
		t.Fatal(err)
	}
	if _, err := encryptLegacyTOTPBatch(ctx, pool, box, 10); err == nil {
		t.Fatal("batch acknowledged an audit failure")
	}
	assertUnchanged()
}
