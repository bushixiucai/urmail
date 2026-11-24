package sqlitedb_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

func openCurrentSettingDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = sqlitedb.Migrate(ctx, db, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}

// TestPasswordUpdateRunsUnderCurrentSettingUDF is the Phase 4 blocker: the
// users trigger calls current_setting('urmail.password_rehash',true), so a
// password change must not fail with "no such function".
func TestPasswordUpdateRunsUnderCurrentSettingUDF(t *testing.T) {
	db, ctx := openCurrentSettingDB(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,status,credential_version) VALUES(1,'u@x.test','h1','active',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET password_hash='h2' WHERE id=1`); err != nil {
		t.Fatalf("password update: %v", err)
	}
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT credential_version FROM users WHERE id=1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("credential_version=%d after a real password change, want 1 (rehash exemption must not apply)", version)
	}
	// MFA changes bump the generation too.
	if _, err := db.ExecContext(ctx, `UPDATE users SET mfa_enabled=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT credential_version FROM users WHERE id=1`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("credential_version=%d err=%v after MFA change, want 2", version, err)
	}
	// An unrelated column never bumps.
	if _, err := db.ExecContext(ctx, `UPDATE users SET email='u2@x.test' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT credential_version FROM users WHERE id=1`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("credential_version=%d err=%v after email change, want 2", version, err)
	}
}

// TestCurrentSettingMatchesPostgresSemantics pins the unset behavior: the
// one-argument form raises, the missing_ok form returns NULL, and the trigger
// guard `IS NOT 'true'` therefore cannot be bypassed.
func TestCurrentSettingMatchesPostgresSemantics(t *testing.T) {
	db, ctx := openCurrentSettingDB(t)
	var value sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT current_setting('urmail.password_rehash',true)`).Scan(&value); err != nil {
		t.Fatalf("missing_ok form: %v", err)
	}
	if value.Valid {
		t.Fatalf("unset setting returned %q, want NULL", value.String)
	}
	var guard int
	if err := db.QueryRowContext(ctx, `SELECT current_setting('urmail.password_rehash',true) IS NOT 'true'`).Scan(&guard); err != nil || guard != 1 {
		t.Fatalf("guard=%d err=%v, want 1 (NULL IS NOT 'true' is true)", guard, err)
	}
	if _, err := db.ExecContext(ctx, `SELECT current_setting('urmail.password_rehash')`); err == nil {
		t.Fatal("one-argument form must raise for an unset setting")
	}
	if _, err := db.ExecContext(ctx, `SELECT current_setting('x',false)`); err == nil {
		t.Fatal("missing_ok=false must raise")
	}
}
