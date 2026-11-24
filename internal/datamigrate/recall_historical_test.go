package datamigrate

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	pgxpool "github.com/jackc/pgx/v5/pgxpool"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/migrations"
)

// TestDeliveredHistoricalSchemaWitness creates a delivered recall row on the
// real in-repo schema at version 17 (before the 0018 write guard, CHECK allows
// delivered), upgrades the same database normally to the chain tip (0160), verifies the row was
// not changed, and imports it through the product staging API: the published
// SQLite row must match all 20 columns.
func TestDeliveredHistoricalSchemaWitness(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)

	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := migratepgx.WithInstance(conn, &migratepgx.Config{})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Migrate(17); err != nil {
		t.Fatalf("migrate synthetic source to v17: %v", err)
	}
	var version int
	if err := conn.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil || version != 17 {
		t.Fatalf("schema version=%d err=%v want 17", version, err)
	}
	seed := []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'hist','Historical')`,
		`INSERT INTO users(id,email,password_hash) VALUES(1,'hist@example.test','x'),(2,'owner@example.test','x')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status,owner_user_id) VALUES(1,1,'Hist','hist','active',1)`,
		`INSERT INTO recall_requests(id,tenant_id,message_id,requested_by,status,reason)
		 VALUES(7001,1,'hist-delivered','2','delivered','remote delivery confirmed before the reviewed guard existed')`,
	}
	for _, stmt := range seed {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("v17 seed %q: %v", stmt, err)
		}
	}
	before := recallHash(t, ctx, dsn, 7001)
	if before.status != "delivered" {
		t.Fatalf("v17 row status=%q", before.status)
	}

	upgrader, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upgrader.Close() }()
	if err := upgrader.Up(ctx); err != nil {
		t.Fatalf("normal upgrade to head rejected the historical row: %v", err)
	}
	if v, _, err := upgrader.Version(); err != nil || v != 160 {
		t.Fatalf("upgraded version=%d err=%v want 160", v, err)
	}
	after := recallHash(t, ctx, dsn, 7001)
	if before != after {
		t.Fatalf("upgrade changed the delivered row: before=%+v after=%+v", before, after)
	}

	// Managed migration role for the product staging source contract.
	for _, stmt := range []string{
		`ALTER ROLE urmail_migrate LOGIN PASSWORD 'synthetic-hist' BYPASSRLS`,
		`GRANT CONNECT ON DATABASE ` + currentDatabase(t, conn) + ` TO urmail_migrate`,
		`GRANT USAGE ON SCHEMA public TO urmail_migrate`,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO urmail_migrate`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("grant %q: %v", stmt, err)
		}
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword("urmail_migrate", "synthetic-hist")
	target := filepath.Join(t.TempDir(), "historical-delivered.sqlite")
	tm := mustTypeMap(t)
	report, err := ImportRecallStaging(ctx, StagingOptions{
		SourceURL:      parsed.String(),
		TargetPath:     target,
		Migrations:     os.DirFS("../../migrations_sqlite"),
		MigrationsRoot: ".",
		TypeMap:        tm,
	})
	if err != nil {
		t.Fatalf("product staging import: %v", err)
	}
	if report.SnapshotIsolation != "repeatable read" || report.SnapshotReadOnly != "on" {
		t.Fatalf("snapshot contract: %+v", report)
	}
	published, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer published.Close()
	vals := make([]any, len(recallColumns))
	ptrs := make([]any, len(vals))
	for i := range ptrs {
		ptrs[i] = &vals[i]
	}
	if err := published.QueryRowContext(ctx,
		"SELECT "+quoteList(recallColumns, false)+" FROM recall_requests WHERE id=7001").Scan(ptrs...); err != nil {
		t.Fatal(err)
	}
	if statusOf(vals) != "delivered" {
		t.Fatalf("published status=%q want delivered", statusOf(vals))
	}
	if messageIDOf(vals[2]) != after.message || valuesEqual(vals[5], after.reason) == false {
		t.Fatalf("published identity drifted: message=%q reason=%v want %q/%q", messageIDOf(vals[2]), vals[5], after.message, after.reason)
	}
}

type recallRowHash struct {
	status      string
	message     string
	reason      string
	createdText string
}

func recallHash(t *testing.T, ctx context.Context, dsn string, id int64) recallRowHash {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var h recallRowHash
	if err := pool.QueryRow(ctx, `SELECT status, message_id, coalesce(reason,''), created_at::text
		FROM recall_requests WHERE id=$1`, id).Scan(&h.status, &h.message, &h.reason, &h.createdText); err != nil {
		t.Fatal(err)
	}
	return h
}

func currentDatabase(t *testing.T, conn *sql.DB) string {
	t.Helper()
	var name string
	if err := conn.QueryRow("SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(name, "\"'") {
		t.Fatalf("unsupported database name")
	}
	return `"` + name + `"`
}

var _ = sqlitedb.ExpectedSchemaVersion
