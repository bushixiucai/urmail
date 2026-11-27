package sqlitedb_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

func applyLegacyV3(t *testing.T, db *sql.DB, legacy bool) {
	t.Helper()
	for _, name := range []string{"0001_init.sql", "0002_write_side_effects.sql", "0003_mailbox_readonly.sql"} {
		sqlBytes, err := os.ReadFile(filepath.Join("..", "..", "migrations_sqlite", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), string(sqlBytes)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	if !legacy {
		return
	}
	// Represent an already-installed v3 database: its domain_tls_reports still
	// carries the pre-fix CHECK (period_start + '8 days' → +8 micros).
	for _, stmt := range []string{
		`DROP TABLE domain_tls_reports`,
		`CREATE TABLE "domain_tls_reports" (
		  "id" INTEGER PRIMARY KEY AUTOINCREMENT, "tenant_id" INTEGER NOT NULL, "domain_id" INTEGER NOT NULL,
		  "report_digest" TEXT NOT NULL, "reporter_digest" TEXT NOT NULL, "report_id_digest" TEXT NOT NULL,
		  "period_start" INTEGER NOT NULL, "period_end" INTEGER NOT NULL, "successful" INTEGER NOT NULL,
		  "failed" INTEGER NOT NULL, "failures" TEXT NOT NULL,
		  "received_at" INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)*1000000),
		  "source_verified" INTEGER NOT NULL DEFAULT 0,
		  UNIQUE (tenant_id, domain_id, reporter_digest, report_id_digest, period_start, period_end),
		  FOREIGN KEY (tenant_id, domain_id) REFERENCES domains(tenant_id, id),
		  FOREIGN KEY (tenant_id) REFERENCES tenants(id),
		  CHECK (period_end >= period_start AND period_end <= (period_start + '8 days')),
		  CHECK (failed >= 0 AND failed <= 1000000000),
		  CHECK (length(report_digest) = 64), CHECK (length(report_id_digest) = 64), CHECK (length(reporter_digest) = 64),
		  CHECK (NOT source_verified), CHECK (successful >= 0 AND successful <= 1000000000))`,
		`CREATE UNIQUE INDEX domain_tls_reports_tenant_id_domain_id_reporter_digest_repo_key
		  ON domain_tls_reports (tenant_id, domain_id, reporter_digest, report_id_digest, period_start, period_end)`,
		`INSERT INTO brands(id,slug,name) VALUES(1,'b','B')`,
		`INSERT INTO users(id,email,password_hash) VALUES(1,'v3@example.test','x')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(1,1,'T','t','active')`,
		`INSERT INTO domains(id,tenant_id,fqdn,verification_token) VALUES(1,1,'tls.example.test','v')`,
		`INSERT INTO domain_tls_reports(tenant_id,domain_id,report_digest,reporter_digest,report_id_digest,
			period_start,period_end,successful,failed,failures)
		 VALUES(1,1,printf('%064d',1),printf('%064d',2),printf('%064d',3),1000,1005,1,0,'[]')`,
	} {
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("legacy fixture %q: %v", stmt, err)
		}
	}
}

func TestUpgradeV3ReceivesCorrectedTLSPeriodCheck(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-v3.sqlite")
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	applyLegacyV3(t, db, true)
	if err := sqlitedb.Migrate(ctx, db, os.DirFS("../../migrations_sqlite"), "."); err != nil {
		t.Fatalf("upgrade v3: %v", err)
	}
	var version int
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != sqlitedb.ExpectedSchemaVersion {
		t.Fatalf("user_version=%d err=%v want %d", version, err, sqlitedb.ExpectedSchemaVersion)
	}
	var rows int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM domain_tls_reports").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("existing rows=%d err=%v", rows, err)
	}
	// Legal product period (8 days) must now be accepted on the upgraded DB;
	// the old CHECK only allowed +8 micros.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO domain_tls_reports(tenant_id,domain_id,report_digest,reporter_digest,report_id_digest,
		period_start,period_end,successful,failed,failures)
		VALUES(1,1,printf('%064d',4),printf('%064d',5),printf('%064d',6),10000,10000+8*24*3600*1000000,1,0,'[]')`); err != nil {
		t.Fatalf("legal 8-day period rejected after upgrade: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO domain_tls_reports(tenant_id,domain_id,report_digest,reporter_digest,report_id_digest,
		period_start,period_end,successful,failed,failures)
		VALUES(1,1,printf('%064d',7),printf('%064d',8),printf('%064d',9),20000,20000+8*24*3600*1000000+1,1,0,'[]')`); err == nil {
		t.Fatal("over-limit period accepted")
	}
	idxRows, err := db.QueryContext(t.Context(), "SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='domain_tls_reports' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer idxRows.Close()
	var indexes []string
	for idxRows.Next() {
		var name string
		if err := idxRows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, name)
	}
	sort.Strings(indexes)
	if !strings.Contains(strings.Join(indexes, ","), "domain_tls_reports_tenant_id_domain_id_reporter_digest_repo_key") {
		t.Fatalf("unique index missing after rebuild: %v", indexes)
	}
	fk, err := db.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer fk.Close()
	if fk.Next() {
		t.Fatal("foreign_key_check reported a violation after upgrade")
	}
}

func TestFailedTLSPeriodRebuildLeavesLegacyIntact(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-v3-bad.sqlite")
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	applyLegacyV3(t, db, true)
	// A pre-existing row outside the corrected window forces the rebuild to
	// fail; it was only insertable by bypassing the legacy CHECK in this
	// synthetic fixture. The migration must roll back completely.
	if _, err := db.ExecContext(t.Context(), "PRAGMA ignore_check_constraints=ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO domain_tls_reports(tenant_id,domain_id,report_digest,reporter_digest,report_id_digest,
		period_start,period_end,successful,failed,failures)
		VALUES(1,1,printf('%064d',11),printf('%064d',12),printf('%064d',13),5000,5000+9*24*3600*1000000,1,0,'[]')`); err != nil {
		t.Fatal(err)
	}
	_, _ = db.ExecContext(t.Context(), "PRAGMA ignore_check_constraints=OFF")
	if err := sqlitedb.Migrate(ctx, db, os.DirFS("../../migrations_sqlite"), "."); err == nil {
		t.Fatal("invalid legacy row did not fail the rebuild")
	}
	var version, rows int
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 5 {
		t.Fatalf("version=%d err=%v want 5 (0006 rolled back) after failed migration", version, err)
	}
	// The legacy CHECK is still in force: no partial rebuild leaked.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO domain_tls_reports(tenant_id,domain_id,report_digest,reporter_digest,report_id_digest,
		period_start,period_end,successful,failed,failures)
		VALUES(1,1,printf('%064d',14),printf('%064d',15),printf('%064d',16),30000,30000+8*24*3600*1000000,1,0,'[]')`); err == nil {
		t.Fatal("failed rebuild left the corrected CHECK behind")
	}
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM domain_tls_reports").Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("rows=%d err=%v want 2 intact", rows, err)
	}
}
