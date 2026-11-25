package reconcile_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

// TestAssembleConfigContextOnSQLite proves the platform queries used by the
// renderer run on the single engine, including the alias sender-login array
// that PostgreSQL renders with ARRAY(SELECT …).
func TestAssembleConfigContextOnSQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "urmail.sqlite")
	raw, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlitedb.Migrate(ctx, raw, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	pool, err := db.NewSQLitePool(raw, isolation.ScopePlatform)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	cfg := &config.Config{MyHostname: "box.example.test", PrimaryDomain: "example.test", StorageRoot: "/var/mail", DatabaseURLDovecot: "host=127.0.0.1 dbname=urmail"}
	if _, err = reconcile.AssembleConfigContext(ctx, cfg, &reconcile.VersionedStore{StoreQuerier: db.Queries(pool), DB: pool}, "192.0.2.1", nil); err != nil {
		t.Fatalf("assemble on sqlite: %v", err)
	}
}
