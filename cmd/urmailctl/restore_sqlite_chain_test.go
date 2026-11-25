package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	_ "modernc.org/sqlite"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

// seedSQLite seeds one migrated database with a brand marker and returns it.
func seedSQLite(t *testing.T, path, marker string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlitedb.Migrate(ctx, db, migrations_sqlite.FS, "."); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO brands(slug,name) VALUES(?,?)`, marker, marker); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db
}

// TestSQLiteRestoreRebuildsAuthSnapshot is the §9.2 chain: a verified snapshot
// replaces the live database, the sidecars are folded, and the dovecot auth
// snapshot is rebuilt from the restored store before the restore may release.
func TestSQLiteRestoreRebuildsAuthSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	livePath := filepath.Join(dir, "live.sqlite")
	sourcePath := filepath.Join(dir, "source.sqlite")
	snapshotPath := filepath.Join(dir, "urmail-20261125T000000Z.sqlite")
	authPath := filepath.Join(dir, "dovecot-auth.db")

	live := seedSQLite(t, livePath, "live-before-restore")
	defer func() { _ = live.Close() }()
	source := seedSQLite(t, sourcePath, "from-snapshot")
	if _, err := source.ExecContext(ctx, `VACUUM INTO ?`, snapshotPath); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{DBEngine: "sqlite", SQLitePath: livePath, DovecotAuthDB: authPath}
	if err := restoreSQLiteDatabase(ctx, cfg, snapshotPath); err != nil {
		t.Fatalf("sqlite restore chain: %v", err)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		matches, globErr := filepath.Glob(livePath + suffix)
		if globErr != nil {
			t.Fatal(globErr)
		}
		if len(matches) != 0 {
			t.Fatalf("restore left a %s sidecar behind: %v", suffix, matches)
		}
	}
	restored, err := sql.Open("sqlite", "file:"+livePath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	var count int
	if err = restored.QueryRowContext(ctx, `SELECT count(*) FROM brands WHERE slug='from-snapshot'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restored brand count=%d err=%v", count, err)
	}
	if err = restored.QueryRowContext(ctx, `SELECT count(*) FROM brands WHERE slug='live-before-restore'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pre-restore generation survived: count=%d err=%v", count, err)
	}
	var version int
	if err = restored.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != sqlitedb.ExpectedSchemaVersion {
		t.Fatalf("restored user_version=%d err=%v", version, err)
	}

	// The restore gate rebuilt the auth snapshot from the restored store.
	auth, err := sql.Open("sqlite", "file:"+authPath)
	if err != nil {
		t.Fatalf("auth snapshot missing after restore: %v", err)
	}
	defer func() { _ = auth.Close() }()
	var authVersion, tenants int
	if err = auth.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&authVersion); err != nil || authVersion != 1 {
		t.Fatalf("auth snapshot user_version=%d err=%v", authVersion, err)
	}
	if err = auth.QueryRowContext(ctx, `SELECT count(*) FROM tenants`).Scan(&tenants); err != nil {
		t.Fatalf("auth snapshot unreadable: %v", err)
	}
}

// TestSQLiteRestoreRefusesMissingLivePath keeps the refusal before any file is
// replaced.
func TestSQLiteRestoreRefusesMissingLivePath(t *testing.T) {
	err := restoreSQLiteDatabase(context.Background(), &config.Config{DBEngine: "sqlite"}, "/tmp/snapshot.sqlite")
	if err == nil {
		t.Fatal("restore without a live path was accepted")
	}
}

// TestSQLiteBackupOffsiteRestoreFullChain exercises the complete S7.5 offline
// chain: VACUUM INTO snapshot -> age encrypt + local simulated offsite upload/
// download + decrypt -> restore with stale -wal/-shm sidecars cleanup -> PRAGMA
// & user_version assertions -> dovecot auth snapshot rebuild, plus QA-2 kind
// verification.
func TestSQLiteBackupOffsiteRestoreFullChain(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "live-before.sqlite")
	livePath := filepath.Join(dir, "live-after.sqlite")
	authPath := filepath.Join(dir, "dovecot-auth.db")
	snapPath := filepath.Join(dir, "urmail-20261126T000000Z.sqlite")
	offsiteDir := filepath.Join(dir, "offsite-object-store")
	if err := os.MkdirAll(offsiteDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// 1. Seed live source database and create VACUUM INTO snapshot.
	src := seedSQLite(t, srcPath, "s75-full-chain")
	if _, err := src.ExecContext(ctx, `INSERT INTO tenants(id, brand_id, name, slug, status) SELECT 42, id, 'acme', 'acme', 'active' FROM brands WHERE slug='s75-full-chain'`); err != nil {
		_ = src.Close()
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if err := createSQLiteSnapshot(ctx, srcPath, snapPath); err != nil {
		t.Fatalf("createSQLiteSnapshot: %v", err)
	}
	snapBytes, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(snapBytes)
	snapSHA := hex.EncodeToString(sum[:])

	// 2. Simulate offsite encrypt -> upload -> download -> decrypt + SHA-256 verify.
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := backupauto.EncryptTo(&encrypted, bytes.NewReader(snapBytes), id.Recipient()); err != nil {
		t.Fatalf("EncryptTo: %v", err)
	}
	remoteObj := filepath.Join(offsiteDir, "urmail-20261126T000000Z.sqlite.age")
	if err := os.WriteFile(remoteObj, encrypted.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	downloaded, err := os.ReadFile(remoteObj)
	if err != nil {
		t.Fatal(err)
	}
	var decrypted bytes.Buffer
	if err := backupauto.DecryptFrom(&decrypted, bytes.NewReader(downloaded), id); err != nil {
		t.Fatalf("DecryptFrom: %v", err)
	}
	gotSum := sha256.Sum256(decrypted.Bytes())
	if hex.EncodeToString(gotSum[:]) != snapSHA {
		t.Fatalf("offsite roundtrip SHA-256 mismatch")
	}
	restoredSnap := filepath.Join(dir, "downloaded-snapshot.sqlite")
	if err := os.WriteFile(restoredSnap, decrypted.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	// 3. Seed target live DB with stale generation and plant dirty -wal/-shm sidecars.
	live := seedSQLite(t, livePath, "stale-generation")
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(livePath+suffix, []byte("stale-sidecar"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 4. Restore into livePath + PRAGMA assertions + forced dovecot auth snapshot rebuild.
	cfg := &config.Config{DBEngine: "sqlite", SQLitePath: livePath, DovecotAuthDB: authPath}
	if err := restoreSQLiteDatabase(ctx, cfg, restoredSnap); err != nil {
		t.Fatalf("restoreSQLiteDatabase: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		matches, globErr := filepath.Glob(livePath + suffix)
		if globErr != nil || len(matches) != 0 {
			t.Fatalf("expected %s sidecar cleaned, got matches=%v err=%v", suffix, matches, globErr)
		}
	}

	reopened, err := sqlitedb.OpenExisting(ctx, livePath, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		t.Fatalf("OpenExisting post-restore: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	var brandCount int
	if err := reopened.QueryRowContext(ctx, `SELECT count(*) FROM brands WHERE slug='s75-full-chain'`).Scan(&brandCount); err != nil || brandCount != 1 {
		t.Fatalf("restored brand count=%d err=%v", brandCount, err)
	}
	var integrity string
	if err := reopened.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity_check=%q err=%v", integrity, err)
	}

	authDB, err := sql.Open("sqlite", "file:"+authPath)
	if err != nil {
		t.Fatalf("open rebuilt auth snapshot: %v", err)
	}
	defer func() { _ = authDB.Close() }()
	var tenantCount int
	if err := authDB.QueryRowContext(ctx, `SELECT count(*) FROM tenants WHERE tenant_id=42 AND status='active'`).Scan(&tenantCount); err != nil || tenantCount != 1 {
		t.Fatalf("rebuilt auth snapshot missing restored tenant: count=%d err=%v", tenantCount, err)
	}
}
