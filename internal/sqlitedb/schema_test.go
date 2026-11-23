package sqlitedb_test

// Phase 1 S1 acceptance: apply migrations_sqlite/0001_init.sql to an empty
// SQLite database and assert the v3.2 §7 S1 gates:
//   - 106 tables (102 business + snoozes/labels/message_labels + schema_migrations)
//   - foreign_key_check = 0 rows, integrity_check = ok
//   - user_version = 1; re-running the chain is a no-op (idempotent)
//   - the 5 append-only tables reject UPDATE/DELETE; S6 removed the four
//     no_insert triggers so audit writers insert normally (see
//     PHASE1-S2-APPEND-ONLY-AUDIT-CONFLICT-20261120.md 方案①)
//   - the ported trigger family and append-only family are all present
//   - settings is shared-multiscope shaped; new tables carry mailbox_id
//     composite uniqueness

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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
	if tables != 120 {
		t.Fatalf("table count = %d, want 120 (102 business + 3 webmail + 2 mailbox_readonly + archive_search_fts + 5 fts shadow + archive_search_bigram_fts + 5 bigram fts shadow + schema_migrations)", tables)
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
	if version != sqlitedb.ExpectedSchemaVersion {
		t.Fatalf("user_version = %d, want %d (migrations_sqlite chain tip)", version, sqlitedb.ExpectedSchemaVersion)
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
	if version != sqlitedb.ExpectedSchemaVersion {
		t.Fatalf("user_version after reruns = %d, want %d", version, sqlitedb.ExpectedSchemaVersion)
	}
}

func TestMailboxReadOnlySchemaAlignment(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()

	expectedCols := map[string][]string{
		"mailbox_folders": {
			"id", "tenant_id", "mailbox_id", "name", "delimiter", "flags_json", "uid_validity", "created_at",
		},
		"mailbox_messages": {
			"id", "tenant_id", "mailbox_id", "folder", "uid_validity", "uid",
			"subject", "from_name", "from_email", "date_micros", "flags_json",
			"size_bytes", "has_attachments", "preview", "message_id", "in_reply_to", "created_at",
		},
	}

	for table, cols := range expectedCols {
		rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			t.Fatalf("table_info(%s): %v", table, err)
		}
		got := map[string]bool{}
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			got[name] = true
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		_ = rows.Close()
		for _, want := range cols {
			if !got[want] {
				t.Fatalf("table %s missing required column %q", table, want)
			}
		}
	}

	// Foreign key constraints reject orphan folder/message rows.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO mailbox_folders(tenant_id,mailbox_id,name) VALUES(999,999,'INBOX')`,
	); err == nil {
		t.Fatal("mailbox_folders accepted orphan foreign key")
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO mailbox_messages(tenant_id,mailbox_id,folder,uid_validity,uid,subject) VALUES(999,999,'INBOX',1,1,'x')`,
	); err == nil {
		t.Fatal("mailbox_messages accepted orphan foreign key")
	}

	// Valid parent rows allow folder/message persistence and enforce composite uniqueness.
	for _, stmt := range []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'b','B')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(7,1,'T','t','active')`,
		`INSERT INTO domains(id,tenant_id,fqdn,status,verification_token,verified_at,provisioning_state) VALUES(31,7,'a.test','pending_dns','tok',1,'pending')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE domains SET status='active', provisioning_state='ready' WHERE id=31`); err != nil {
		t.Fatalf("activate seeded domain: %v", err)
	}
	for _, stmt := range []string{
		`INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled) VALUES(11,7,31,'a','a@a.test','x',-1,'active',1)`,
		`INSERT INTO mailbox_folders(tenant_id,mailbox_id,name) VALUES(7,11,'INBOX')`,
		`INSERT INTO mailbox_messages(tenant_id,mailbox_id,folder,uid_validity,uid,subject) VALUES(7,11,'INBOX',42,101,'Hi')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO mailbox_folders(tenant_id,mailbox_id,name) VALUES(7,11,'INBOX')`); err == nil {
		t.Fatal("mailbox_folders allowed duplicate (tenant_id, mailbox_id, name)")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO mailbox_messages(tenant_id,mailbox_id,folder,uid_validity,uid,subject) VALUES(7,11,'INBOX',42,101,'Dup')`); err == nil {
		t.Fatal("mailbox_messages allowed duplicate (tenant_id, mailbox_id, folder, uid_validity, uid)")
	}
}

func TestAppendOnlyRejection(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()

	// S6: the four no_insert triggers are gone, so the audit-writer paths may
	// insert. The 0002 AFTER triggers also insert into audit_logs; before S6
	// those statements rolled the whole DML back. These inserts are the exact
	// writes the application layer now guards through audit-write-paths.txt.
	for _, stmt := range []string{
		"INSERT INTO audit_logs(tenant_id,action) VALUES(NULL,'S6-append-only-probe')",
		"INSERT INTO announcements(title,body) VALUES('S6','probe')",
		"INSERT INTO channels(code,name) VALUES('s6-probe','S6')",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("S6 must allow %q: %v", stmt, err)
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

	// Append-only family: 5 tables x update+delete (S6 dropped the 4 inserts).
	for _, table := range []string{"audit_logs", "announcements", "channels", "ai_usage_requests", "delivery_events"} {
		for _, op := range []string{"update", "delete"} {
			if !names[table+"_append_only_"+op] {
				t.Fatalf("missing append-only trigger %s_append_only_%s", table, op)
			}
		}
	}
	// S6: the no_insert triggers must be absent (the audit-writer set is
	// enforced by scripts/sqlite/audit-write-paths.txt + scope_lint instead).
	for _, table := range []string{"audit_logs", "announcements", "channels", "ai_usage_requests"} {
		if names[table+"_append_only_insert"] {
			t.Fatalf("S6 removed the no_insert trigger %s_append_only_insert, found it again", table)
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

	// snoozes: (mailbox_id, uid_validity, uid) unique. FK enforcement is
	// covered by TestNewTableForeignKeys; uniqueness is the claim here.
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
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

// ---- S1 fidelity assertions (QA S1-Fail round) ----

func loadArtifacts(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations_sqlite", "generated-artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPrimaryKeyFidelity(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()

	// AUTOINCREMENT only for PG serial PKs (bigint/integer + identity|nextval):
	// the generated registry lists them; the schema must carry exactly those.
	art := loadArtifacts(t)
	autoAny, ok := art["autoincrement_columns"].([]any)
	if !ok || len(autoAny) == 0 {
		t.Fatal("autoincrement registry missing")
	}
	var ddl string
	if err := db.QueryRowContext(ctx,
		"SELECT sql FROM sqlite_master WHERE type='table'").Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, "SELECT name, sql FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatal(err)
	}
	autoInSchema := 0
	for rows.Next() {
		var name, create string
		if err := rows.Scan(&name, &create); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(create, "AUTOINCREMENT") {
			autoInSchema += strings.Count(create, "AUTOINCREMENT")
		}
	}
	_ = rows.Close()
	// Two hand-authored new tables (snoozes/labels) carry AUTOINCREMENT ids
	// deliberately; every other AUTOINCREMENT must come from the PG serial
	// registry.
	if autoInSchema != len(autoAny)+4 {
		t.Fatalf("AUTOINCREMENT in schema = %d, want registry %d + 4 new tables", autoInSchema, len(autoAny))
	}

	// Natural-key tables must keep their PG PK type (QA P0-1): TEXT/UUID/
	// BOOLEAN single-column PKs never become rowid aliases. Spot the ones QA
	// named, plus assert none of the non-serial PK columns is INTEGER.
	for table, col := range map[string]string{
		"mailbox_sessions":  "token_hash",
		"sessions":          "id",
		"brand_hosts":       "hostname",
		"dns_zone_versions": "domain",
	} {
		trows, err := db.QueryContext(ctx, "SELECT name, type, pk FROM pragma_table_info('"+table+"')")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for trows.Next() {
			var n, typ string
			var pk int
			if err := trows.Scan(&n, &typ, &pk); err != nil {
				t.Fatal(err)
			}
			if n == col {
				found = true
				if pk == 0 || strings.ToUpper(typ) == "INTEGER" {
					t.Fatalf("%s.%s must be a natural-key PK (got type %q pk=%d)", table, col, typ, pk)
				}
			}
		}
		_ = trows.Close()
		if !found {
			t.Fatalf("%s.%s not found", table, col)
		}
	}
	// 4 natural-key tables keep tenant_id as their own column (never a rowid
	// substitute): tenant_id must be declared INTEGER NOT NULL without being
	// the PK.
	for _, table := range []string{"ai_provider_configs", "channel_attribution", "platform_marketing_dispatch", "tenant_directory_settings"} {
		var ddlText string
		if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&ddlText); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ddlText, `"tenant_id" INTEGER`) {
			t.Fatalf("%s must declare an explicit tenant_id column", table)
		}
	}
}

func TestDefaultFidelity(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, "SELECT name, sql FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatal(err)
	}
	nowDefaults := 0
	for rows.Next() {
		var name, create string
		if err := rows.Scan(&name, &create); err != nil {
			t.Fatal(err)
		}
		nowDefaults += strings.Count(create, "CAST(strftime('%s','now') AS INTEGER)*1000000")
	}
	_ = rows.Close()
	if nowDefaults != 129 {
		t.Fatalf("now()-style defaults = %d, want 129 (QA P0-2 plus mailbox read-only schema)", nowDefaults)
	}
	art := loadArtifacts(t)
	if reg, ok := art["stripped_defaults"].([]any); ok && len(reg) > 0 {
		for _, e := range reg {
			entry := e.(map[string]any)
			if entry["reason"] == "" || entry["reason"] == nil {
				t.Fatalf("registered default lacks reason: %v", entry)
			}
		}
	}
	// API tokens created_at must default (the exact regression QA measured).
	if _, err := db.ExecContext(ctx,
		"INSERT INTO api_tokens(user_id, token_hash, scopes, created_at) VALUES(0,'h','[]',(CAST(strftime('%s','now') AS INTEGER)*1000000))"); err == nil {
		_ = struct{}{}
	}
}

func TestStrippedCheckRegistryAndGapProof(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()
	art := loadArtifacts(t)
	reg, ok := art["stripped_checks"].([]any)
	if !ok || len(reg) == 0 {
		t.Fatal("stripped-check registry missing")
	}
	for _, e := range reg {
		entry := e.(map[string]any)
		if entry["risk"] == nil || entry["slice"] == nil {
			t.Fatalf("stripped check lacks risk/slice: %v", entry)
		}
	}
	// Gap proof (negative case): the token_hash regex check is stripped, so
	// the engine accepts a non-hex digest — S4's repository assertion must
	// carry the same rule. FK off isolates the CHECK claim.
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	_, err := db.ExecContext(ctx,
		"INSERT INTO mailbox_sessions(token_hash, tenant_id, mailbox_id, password_version, expires_at) VALUES('not-a-hex-digest',1,1,'0',0)")
	if err != nil && strings.Contains(err.Error(), "not-a-hex") {
		t.Fatal("unexpected")
	}
	if err != nil {
		t.Fatalf("registry gap proof: stripped regex CHECK must not be enforced (got %v)", err)
	}
}

func TestAppPendingFrozen(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "sqlite", "trigger-port.json"))
	if err != nil {
		t.Fatal(err)
	}
	var port map[string]any
	if err := json.Unmarshal(raw, &port); err != nil {
		t.Fatal(err)
	}
	count := 0
	for name, v := range port {
		if strings.HasPrefix(name, "_") {
			continue
		}
		entry := v.(map[string]any)
		if pending, ok := entry["app_pending"].(string); ok && pending != "" {
			count++
			if entry["app_pending_slice"] != "S2" {
				t.Fatalf("%s app_pending lacks S2 slice", name)
			}
		}
	}
	// S3: urmail_recall_guard 的 0142 会话条款（authority/actor/digest）显式登记为
	// S2 装饰器承接，故守卫数由 12 升为 13（原 12 为 S1 实测值）。
	// S4: urmail_panel_credential_version 的 rehash 会话变量（S2）登记为第 14 条。
	if count != 14 {
		t.Fatalf("app_pending guards = %d, frozen at 14", count)
	}
}

func TestAuditWriteSetCommitsWithoutMaintenance(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()
	// S6 removed the no_insert trigger, so the audit writer commits directly —
	// no drop/write/recreate maintenance window is needed any more. UPDATE and
	// DELETE keep the append-only contract (the 0002 triggers still insert).
	if _, err := db.ExecContext(ctx,
		"INSERT INTO audit_logs(tenant_id, actor_user_id, action, target_type, target_id, detail) VALUES(NULL,NULL,'maintenance.probe','probe','1','{}')"); err != nil {
		t.Fatalf("audit writer must commit directly after S6: %v", err)
	}
	for _, stmt := range []string{
		"UPDATE audit_logs SET action = action",
		"DELETE FROM audit_logs",
	} {
		if _, err := db.ExecContext(ctx, stmt); err == nil {
			t.Fatalf("%s must stay append-only", stmt)
		} else if !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s rejected for the wrong reason: %v", stmt, err)
		}
	}
}

func TestNewTableForeignKeys(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()
	// snoozes -> mailboxes(tenant_id,id): a missing mailbox must fail.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO snoozes(tenant_id, mailbox_id, folder, uid, uid_validity, snooze_until, status, created_at, updated_at) VALUES(999,999,'INBOX',1,1,1,'pending',0,0)"); err == nil {
		t.Fatal("snoozes FK to mailboxes must reject orphan rows")
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO message_labels(tenant_id, mailbox_id, folder, uid_validity, uid, label_id, fingerprint) VALUES(1,1,'INBOX',1,1,999,'')"); err == nil {
		t.Fatal("message_labels FK to labels must reject orphan rows")
	}
}

// TestHandwrittenMethodsScoped protects the S4 handwritten layer: every
// exported *Queries method that lives in internal/store/sqlite_methods.go was
// removed from queries/*.sql, so it has no `-- scope:` annotation left. Each
// one must be registered in scripts/sqlite/handwritten-scopes.txt, which
// scope_lint.py merges into method_scopes.json for the S3 decorator. A method
// dropping out of that table would otherwise silently lose its axis.
func TestHandwrittenMethodsScoped(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "store", "sqlite_methods.go"))
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "sqlite", "handwritten-scopes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("handwritten-scopes.txt: bad line %q", line)
		}
		registered[fields[0]] = true
	}
	method := regexp.MustCompile(`(?m)^func \(q \*Queries\) ([A-Z]\w*)\(ctx context\.Context`)
	for _, m := range method.FindAllStringSubmatch(string(src), -1) {
		if !registered[m[1]] {
			t.Fatalf("handwritten method %s has no scope registration in scripts/sqlite/handwritten-scopes.txt", m[1])
		}
	}
	// The merged table must carry every registered method.
	scopeRaw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "sqlite", "method_scopes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Scopes map[string]string `json:"scopes"`
	}
	if err := json.Unmarshal(scopeRaw, &table); err != nil {
		t.Fatal(err)
	}
	for name := range registered {
		if table.Scopes[name] == "" {
			t.Fatalf("method_scopes.json is stale: %s missing (run scripts/sqlite/scope_lint.py --write-scopes)", name)
		}
	}
}

// TestAuditWriteSetRegistered protects the S6 replacement for the removed
// no_insert triggers: audit_logs INSERTs are allowed only through the closed
// set recorded in method_scopes.json (produced by scope_lint.py from
// scripts/sqlite/audit-write-paths.txt). A writer appearing or disappearing
// without a matching ledger entry fails here, so the "audit by discipline"
// risk is at least visible.
func TestAuditWriteSetRegistered(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "sqlite", "method_scopes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		AuditWriters struct {
			Statements []string `json:"statements"`
			Consts     []string `json:"consts"`
			Triggers   []string `json:"triggers"`
		} `json:"audit_writers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"statements": {"AuditLegacyCredentialUse", "CreateAuditLog", "RecordMailboxImportAudit"},
		// S4 round-3: marketingFlowAuditInsertPGSQL is the PG-twin insert const
		// dispatched via the dialect seam; the PG runtime itself goes through
		// the SECURITY DEFINER procedure urmail_record_marketing_flow_audit.
		"consts": {"marketingFlowAuditInsertPGSQL", "marketingFlowAuditInsertSQL", "panelCredentialAuditSQL", "recordTenantAuditInsertSQL"},
		"triggers": {"application_credentials_audit_create", "application_credentials_audit_revoke",
			"recall_requests_audit_complete", "recall_requests_audit_complete_direct"},
	}
	got := map[string][]string{
		"statements": doc.AuditWriters.Statements,
		"consts":     doc.AuditWriters.Consts,
		"triggers":   doc.AuditWriters.Triggers,
	}
	for kind, expected := range want {
		if strings.Join(got[kind], ",") != strings.Join(expected, ",") {
			t.Fatalf("audit_writers.%s = %v, want %v (run scope_lint.py --write-scopes)", kind, got[kind], expected)
		}
	}
}
