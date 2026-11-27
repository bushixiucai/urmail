package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

// TestSQLiteWiringStartsDovecotAuthSnapshot proves cutover-blockers
// Blocker A: the slice-1 branch wires the auth snapshot manager with a
// SQLite source, so a rebuild against the live store publishes the
// snapshot file (previously the early return left the starter dead and
// dovecot auth on a stale file — the DevOps self-check observed zero
// snapshot activity).
func TestSQLiteWiringStartsDovecotAuthSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "slice1.db")
	raw, err := sqlitedb.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlitedb.Migrate(ctx, raw, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO brands(id,slug,name) VALUES(1,'b','B')`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(7,1,'T','t','active')`); err != nil {
		t.Fatal(err)
	}
	// domain_provisioning_insert_pending forces pending on INSERT; model a
	// provisioned domain with an explicit UPDATE (same as the slice-1
	// fixtures).
	if _, err = raw.ExecContext(ctx, `INSERT INTO domains(id,tenant_id,fqdn,status,verification_token,verified_at,provisioning_state) VALUES(31,7,'a.example.test','active','tok',1737412800123456,'pending')`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `UPDATE domains SET provisioning_state='ready' WHERE id=31`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled) VALUES(11,7,31,'alice','alice@a.example.test','h',8192,'active',1)`); err != nil {
		t.Fatal(err)
	}

	snapPath := filepath.Join(dir, "dovecot-auth", "dovecot-auth.db")
	cfg := &config.Config{DBEngine: "sqlite", SQLitePath: dbPath, DovecotAuthDB: snapPath}
	startDovecotAuthSnapshot(ctx, cfg, sqliteDovecotAuthSource{db: raw}, zap.NewNop())

	// The manager publishes the snapshot synchronously during wiring; wait
	// briefly for the fs to settle and assert it serves the seeded mailbox.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(snapPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("auth snapshot %s never published", snapPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
	reader, err := sql.Open("sqlite", "file:"+snapPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open published snapshot: %v", err)
	}
	defer func() { _ = reader.Close() }()
	var address string
	if err := reader.QueryRowContext(ctx,
		`SELECT address FROM mailboxes WHERE address = 'alice@a.example.test'`).Scan(&address); err != nil {
		t.Fatalf("snapshot missing the seeded mailbox: %v", err)
	}
	if address != "alice@a.example.test" {
		t.Fatalf("snapshot address %q", address)
	}
	_ = sql.Open
}
