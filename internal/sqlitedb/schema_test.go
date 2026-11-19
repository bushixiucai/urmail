package sqlitedb_test

// Phase 1 S1 acceptance: apply migrations_sqlite/0001_init.sql to an empty
// SQLite database and assert the v3.2 §7 S1 gates:
//   - 106 tables (102 business + snoozes/labels/message_labels + schema_migrations)
//   - foreign_key_check = 0 rows, integrity_check = ok
//   - user_version = 1; re-running the chain is a no-op (idempotent)
//   - the 5 append-only tables reject (INSERT where the app never had the
//     grant; UPDATE/DELETE on a seeded delivery_events row)
//   - the ported trigger family and append-only family are all present
//   - settings is shared-multiscope shaped; new tables carry mailbox_id
//     composite uniqueness

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.db")
	db, err := sqlitedb.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrations := os.DirFS(filepath.Join("..", "..", "migrations_sqlite"))
	if err := sqlitedb.Migrate(context.Background(), db, migrations, "."); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSQLiteSchemaHardAssertions(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()

	var tables int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 106 {
		t.Fatalf("table count = %d, want 106 (102 business + 3 new + schema_migrations)", tables)
	}

	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	fkViolations := 0
	for rows.Next() {
		fkViolations++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if fkViolations != 0 {
		t.Fatalf("foreign_key_check reported %d violations", fkViolations)
	}

	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q", integrity)
	}

	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("user_version = %d, want 1", version)
	}

	// Idempotency: re-running the chain on the same database is a no-op.
	migrations := os.DirFS(filepath.Join("..", "..", "migrations_sqlite"))
	for i := 0; i < 2; i++ {
		if err := sqlitedb.Migrate(ctx, db, migrations, "."); err != nil {
			t.Fatalf("rerun migrate #%d: %v", i+1, err)
		}
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("user_version after reruns = %d, want 1", version)
	}
}

func TestAppendOnlyRejection(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()

	// The four needs-no-insert tables: any INSERT hits the RAISE trigger
	// before NOT NULL evaluation, so DEFAULT VALUES is enough to prove it.
	for _, table := range []string{"audit_logs", "announcements", "channels", "ai_usage_requests"} {
		if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" DEFAULT VALUES"); err == nil {
			t.Fatalf("%s INSERT must hit the append-only trigger", table)
		} else if !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s INSERT rejected for the wrong reason: %v", table, err)
		}
	}

	// delivery_events is app-writable (event writer) but immutable once stored.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO delivery_events(tenant_id, instance, queue_id, message_id, sender, sender_domain, recipient, recipient_domain, status, occurred_at, delay_seconds, response_detail, source_event_id, lifecycle_id) VALUES(NULL,'probe','q','m','s@x','x','r@x','x','queued',0,0.0,'','src-1','life-1')"); err != nil {
		t.Fatalf("delivery_events INSERT must be allowed: %v", err)
	}
	for _, stmt := range []string{
		"UPDATE delivery_events SET id = id",
		"DELETE FROM delivery_events",
	} {
		if _, err := db.ExecContext(ctx, stmt); err == nil {
			t.Fatalf("%s must hit the append-only trigger", stmt)
		} else if !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s rejected for the wrong reason: %v", stmt, err)
		}
	}
}

func TestTriggerPortFamily(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()
	rows, err := db.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='trigger' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names[n] = true
	}
	_ = rows.Close()

	// Append-only family: 5 tables x update+delete (+4 inserts).
	for _, table := range []string{"audit_logs", "announcements", "channels", "ai_usage_requests", "delivery_events"} {
		for _, op := range []string{"update", "delete"} {
			if !names[table+"_append_only_"+op] {
				t.Fatalf("missing append-only trigger %s_append_only_%s", table, op)
			}
		}
	}
	for _, table := range []string{"audit_logs", "announcements", "channels", "ai_usage_requests"} {
		if !names[table+"_append_only_insert"] {
			t.Fatalf("missing no-insert trigger %s_append_only_insert", table)
		}
	}

	// Spot the ported guard family by mapped names (per trigger-port.json).
	for _, want := range []string{
		"totp_encrypted_writes", "recall_state_guard_insert", "recall_state_guard_update",
		"mailbox_storage_identity", "mailbox_storage_reserve", "contact_group_member_scope",
		"domains_host_namespace_guard", "brand_hosts_namespace_guard",
		"guard_marketing_version", "guard_published_marketing_step_delete",
		"retention_confirmation_guard", "suppression_reason_guard",
		"ticket_messages_open_guard", "domains_touch_updated_at",
	} {
		if !names[want] && !names[strings.ReplaceAll(want, "_", "__")] && !names[want+"__insert"] && !names[want+"__update"] {
			// Single-event splits carry __event suffixes; accept any split.
			found := false
			for n := range names {
				if strings.HasPrefix(n, want) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("missing ported trigger for %s", want)
			}
		}
	}
}

func TestMultiscopeSettingsAndNewTables(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()
	for _, stmt := range []string{
		"INSERT INTO settings(scope, scope_id, key, value, updated_at) VALUES('platform', 0, 'k', '{}', 0)",
		"INSERT INTO settings(scope, scope_id, key, value, updated_at) VALUES('tenant', 7, 'k', '{}', 0)",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("settings multiscope insert must pass: %v (stmt %q)", err, stmt)
		}
	}

	// platform_admins: engine-level writes are not denied (SQLite has no
	// grants) — the app write axis denial lives in the S0-c registered
	// revocation and the S2 decorator. The table must still exist.
	var one int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM platform_admins").Scan(&one); err != nil {
		t.Fatalf("platform_admins readable: %v", err)
	}

	// snoozes: (mailbox_id, uid_validity, uid) unique.
	ins := "INSERT INTO snoozes(tenant_id, mailbox_id, folder, uid, uid_validity, snooze_until, status, created_at, updated_at) VALUES(1,1,'INBOX',5,100,200,'pending',0,0)"
	if _, err := db.ExecContext(ctx, ins); err != nil {
		t.Fatalf("snoozes insert: %v", err)
	}
	if _, err := db.ExecContext(ctx, ins); err == nil {
		t.Fatal("snoozes (mailbox,uidvalidity,uid) uniqueness must reject duplicates")
	}

	// labels: (mailbox_id, name) unique.
	insL := "INSERT INTO labels(tenant_id, mailbox_id, name, color_idx, sort, created_at, updated_at) VALUES(1,1,'work',1,0,0,0)"
	if _, err := db.ExecContext(ctx, insL); err != nil {
		t.Fatalf("labels insert: %v", err)
	}
	if _, err := db.ExecContext(ctx, insL); err == nil {
		t.Fatal("labels (mailbox,name) uniqueness must reject duplicates")
	}

	// mailbox_scheduled_sends ALTER columns exist and do not collide with the
	// 0076 composite key.
	var cnt int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM pragma_table_info('mailbox_scheduled_sends') WHERE name IN ('scheduled_at','slot_tz','retry_count')").Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 3 {
		t.Fatalf("mailbox_scheduled_sends ALTER columns = %d, want 3", cnt)
	}
}
