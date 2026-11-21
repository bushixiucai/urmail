package sqlitedb_test

// Phase 1 S5 acceptance: the PG builtin UDFs the query layer depends on must
// exist on every pooled connection, and md5 must be byte-for-byte identical to
// PG's md5(text) (see PHASE1-S2-PG-BUILTIN-FUNCS-AUDIT-20261120.md).

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// md5 test vectors were produced independently:
//
//	psql -Atc "SELECT md5('test123'), md5(''), md5('测试')"
func TestMD5UDFMatchesPG(t *testing.T) {
	db, err := sqlitedb.Open(context.Background(), filepath.Join(t.TempDir(), "md5.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	cases := []struct {
		expr, want string
	}{
		{`md5('test123')`, "cc03e747a6afbbcbf8be7668acfebee5"},
		{`md5('')`, "d41d8cd98f00b204e9800998ecf8427e"},
		{`md5('测试')`, "db06c78d1e24cf708a14ce81c9b617ec"},
		{`md5('a"b\c' || char(10) || 'd')`, "06d844810877eeca25e3da36fa87a221"},
	}
	for _, tc := range cases {
		var got string
		if err := db.QueryRow("SELECT " + tc.expr).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.expr, err)
		}
		if got != tc.want {
			t.Errorf("%s = %s, want %s", tc.expr, got, tc.want)
		}
	}

	// NULL in, NULL out — comparing a NULL password hash must never match a
	// digest of the empty string.
	var null sql.NullString
	if err := db.QueryRow("SELECT md5(NULL)").Scan(&null); err != nil {
		t.Fatalf("md5(NULL): %v", err)
	}
	if null.Valid {
		t.Errorf("md5(NULL) = %q, want NULL", null.String)
	}

	// PG has md5(text) but no md5(integer); refuse rather than hash "1".
	if _, err := db.Exec("SELECT md5(1)"); err == nil {
		t.Error("md5(integer) must fail like PG's undefined function")
	}
}

func TestOpenExistingStartupVerification(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// 1. Missing database must fail and never create an empty file.
	missing := filepath.Join(dir, "missing.sqlite")
	if _, err := sqlitedb.OpenExisting(ctx, missing, sqlitedb.ExpectedSchemaVersion); err == nil {
		t.Fatal("OpenExisting accepted missing sqlite path")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("OpenExisting created or left missing path %s (err=%v)", missing, err)
	}

	// 2. Unmigrated database (user_version=0) must fail closed.
	unmigrated := filepath.Join(dir, "unmigrated.sqlite")
	rawDB, err := sqlitedb.Open(ctx, unmigrated)
	if err != nil {
		t.Fatalf("open unmigrated: %v", err)
	}
	_ = rawDB.Close()
	if _, err := sqlitedb.OpenExisting(ctx, unmigrated, sqlitedb.ExpectedSchemaVersion); err == nil || !strings.Contains(err.Error(), "user_version = 0") {
		t.Fatalf("expected user_version=0 rejection, got %v", err)
	}

	// 3. Symlink path must be refused.
	target := filepath.Join(dir, "valid.sqlite")
	validDB, err := sqlitedb.Open(ctx, target)
	if err != nil {
		t.Fatalf("open valid: %v", err)
	}
	migrations := os.DirFS(filepath.Join("..", "..", "migrations_sqlite"))
	if err := sqlitedb.Migrate(ctx, validDB, migrations, "."); err != nil {
		t.Fatalf("migrate valid: %v", err)
	}
	_ = validDB.Close()

	link := filepath.Join(dir, "link.sqlite")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := sqlitedb.OpenExisting(ctx, link, sqlitedb.ExpectedSchemaVersion); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}

	// 4. Version mismatch / gap (want version+1) must fail closed.
	wantVersionMsg := fmt.Sprintf("user_version = %d", sqlitedb.ExpectedSchemaVersion)
	if _, err := sqlitedb.OpenExisting(ctx, target, sqlitedb.ExpectedSchemaVersion+1); err == nil || !strings.Contains(err.Error(), wantVersionMsg) {
		t.Fatalf("expected version gap rejection, got %v", err)
	}

	// 5. Dirty schema_migrations row must fail closed.
	dirtyCopy := filepath.Join(dir, "dirty.sqlite")
	rawBytes, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if err := os.WriteFile(dirtyCopy, rawBytes, 0o600); err != nil {
		t.Fatalf("write dirty copy: %v", err)
	}
	dDB, err := sqlitedb.Open(ctx, dirtyCopy)
	if err != nil {
		t.Fatalf("open dirty copy: %v", err)
	}
	if _, err := dDB.ExecContext(ctx, "UPDATE schema_migrations SET dirty = 1"); err != nil {
		t.Fatalf("mark dirty: %v", err)
	}
	_ = dDB.Close()
	if _, err := sqlitedb.OpenExisting(ctx, dirtyCopy, sqlitedb.ExpectedSchemaVersion); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("expected dirty migration rejection, got %v", err)
	}

	// 6. Valid migrated database opens cleanly and passes AssertPragmas.
	checked, err := sqlitedb.OpenExisting(ctx, target, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		t.Fatalf("OpenExisting valid: %v", err)
	}
	defer func() { _ = checked.Close() }()
	if err := sqlitedb.AssertPragmas(ctx, checked); err != nil {
		t.Fatalf("AssertPragmas: %v", err)
	}
}
