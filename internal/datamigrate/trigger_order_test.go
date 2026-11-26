package datamigrate

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// TestTriggerInsertDependenciesAreTopologicallyOrdered parses every SQLite
// migration trigger and verifies that every BEFORE INSERT positive cross-table
// reference is declared in triggerInsertDeps (or FKs) and placed before the
// trigger's table by topoSortFK.
func TestTriggerInsertDependenciesAreTopologicallyOrdered(t *testing.T) {
	tmPath, err := filepath.Abs(filepath.Join("..", "..", "migrations_sqlite", "type-map.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	tm, err := LoadTypeMap(tmPath)
	if err != nil {
		t.Fatalf("LoadTypeMap: %v", err)
	}

	sqlDir := filepath.Join("..", "..", "migrations_sqlite")
	files, err := filepath.Glob(filepath.Join(sqlDir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)

	var combined strings.Builder
	for _, f := range files {
		raw, err := os.ReadFile(f) // #nosec G304 -- fixed repo migration glob
		if err != nil {
			t.Fatal(err)
		}
		combined.Write(raw)
		combined.WriteByte('\n')
	}

	trigRe := regexp.MustCompile(`(?is)CREATE\s+TRIGGER\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-zA-Z0-9_]+)\s+(BEFORE|AFTER|INSTEAD\s+OF)\s+(INSERT|UPDATE|DELETE)(?:\s+OF\s+[^\n]+?)?\s+ON\s+([a-zA-Z0-9_]+)(.*?)\bEND\s*;`)
	refRe := regexp.MustCompile(`(?i)\b(?:FROM|JOIN|INTO|UPDATE)\s+([a-zA-Z0-9_]+)`)

	// Mutual-exclusion / negative-guard checks do not require the referenced
	// table to be populated beforehand.
	negativeExclusionTriggers := map[string]bool{
		"domains_host_namespace_guard__insert": true,
		"brand_hosts_namespace_guard__insert":  true,
		"tenant_restore_quarantine_guard":      true,
		"domain_restore_quarantine_guard":      true,
		"mailbox_restore_quarantine_guard":     true,
	}

	plan := make([]TableInfo, 0, len(tm))
	for _, tbl := range tm.Tables() {
		plan = append(plan, TableInfo{Name: tbl})
	}

	ordered, err := topoSortFK(plan)
	if err != nil {
		t.Fatalf("topoSortFK: %v", err)
	}
	pos := make(map[string]int, len(ordered))
	for i, ti := range ordered {
		pos[ti.Name] = i
	}

	matches := trigRe.FindAllStringSubmatch(combined.String(), -1)
	if len(matches) < 83 {
		t.Fatalf("expected >= 83 SQLite triggers, got %d", len(matches))
	}

	for _, m := range matches {
		trigName := m[1]
		timing := strings.ToUpper(m[2])
		op := strings.ToUpper(m[3])
		onTable := strings.ToLower(m[4])
		body := m[5]
		if timing != "BEFORE" || op != "INSERT" || negativeExclusionTriggers[trigName] {
			continue
		}
		if _, ok := tm[onTable]; !ok {
			continue
		}
		for _, rm := range refRe.FindAllStringSubmatch(body, -1) {
			ref := strings.ToLower(rm[1])
			if ref == onTable {
				continue
			}
			if _, ok := tm[ref]; !ok {
				continue
			}
			// Verify triggerInsertDeps covers it and topoSortFK places ref first.
			declared := false
			for _, d := range triggerInsertDeps[onTable] {
				if d == ref {
					declared = true
					break
				}
			}
			if !declared {
				t.Errorf("trigger %s (BEFORE INSERT ON %s) references %s, missing from triggerInsertDeps", trigName, onTable, ref)
			}
			if pos[ref] >= pos[onTable] {
				t.Errorf("trigger %s: referenced table %s (pos %d) must precede %s (pos %d)", trigName, ref, pos[ref], onTable, pos[onTable])
			}
		}
	}

	// Explicit regression check for R2 #11: mailboxes must precede mailbox_aliases.
	if pos["mailboxes"] >= pos["mailbox_aliases"] {
		t.Fatalf("mailboxes (pos %d) must precede mailbox_aliases (pos %d)", pos["mailboxes"], pos["mailbox_aliases"])
	}
}

func TestSuspendAndRestoreAllTriggersExactByteParityAndProbe(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "s5_triggers.sqlite")
	rawDB, err := sqlitedb.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("sqlitedb.Open: %v", err)
	}
	defer func() { _ = rawDB.Close() }()
	if err := sqlitedb.Migrate(ctx, rawDB, os.DirFS("../.."), "migrations_sqlite"); err != nil {
		t.Fatalf("sqlitedb.Migrate: %v", err)
	}

	tx, err := rawDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	target := &Target{DB: sqlTxHandle{tx: tx}}

	before, err := snapshotTriggers(ctx, target)
	if err != nil {
		t.Fatalf("snapshotTriggers(before): %v", err)
	}
	if len(before) < 97 {
		t.Fatalf("expected >= 97 non-recall triggers in migrated SQLite schema, got %d", len(before))
	}
	var totalTriggers int
	if err := target.DB.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='trigger'").Scan(&totalTriggers); err != nil {
		t.Fatal(err)
	}
	if totalTriggers < 103 {
		t.Fatalf("expected >= 103 total triggers in migrated SQLite schema, got %d", totalTriggers)
	}

	suspended, err := suspendAllTriggers(ctx, target)
	if err != nil {
		t.Fatalf("suspendAllTriggers: %v", err)
	}
	mid, err := snapshotTriggers(ctx, target)
	if err != nil {
		t.Fatalf("snapshotTriggers(mid): %v", err)
	}
	if len(mid) != 0 {
		t.Fatalf("expected 0 active triggers while suspended, got %d", len(mid))
	}

	// Historical rows with password_version > 0 must land cleanly while suspended.
	if _, err := target.DB.ExecContext(ctx,
		`INSERT INTO brands(id, slug, name) VALUES(1, 'b1', 'b1');
		 INSERT INTO tenants(id, brand_id, name, slug, status) VALUES(1, 1, 't1', 't1', 'active');
		 INSERT INTO domains(id, tenant_id, fqdn, verification_token, status, verified_at, provisioning_state) VALUES(1, 1, 'example.test', 'tok', 'active', 1, 'ready');
		 INSERT INTO mailboxes(id, tenant_id, domain_id, local_part, address, password_hash, quota_bytes, password_version, password_revoke_pending)
		 VALUES(7, 1, 1, 'tiger', 'tiger@example.test', 'hash', -1, 2, 0);`); err != nil {
		t.Fatalf("insert historical mailbox while triggers suspended: %v", err)
	}

	if err := restoreAndVerifyAllTriggers(ctx, target, suspended); err != nil {
		t.Fatalf("restoreAndVerifyAllTriggers: %v", err)
	}

	after, err := snapshotTriggers(ctx, target)
	if err != nil {
		t.Fatalf("snapshotTriggers(after): %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("trigger count mismatch: got %d, want %d", len(after), len(before))
	}
	for i := range before {
		if after[i].name != before[i].name || after[i].sqlText != before[i].sqlText {
			t.Fatalf("trigger %s mismatch after restore:\ngot:  %q\nwant: %q", before[i].name, after[i].sqlText, before[i].sqlText)
		}
	}

	// Confirm the guard is live again after restore.
	if _, err := target.DB.ExecContext(ctx,
		`INSERT INTO mailboxes(tenant_id, domain_id, local_part, address, password_hash, quota_bytes, password_version, password_revoke_pending)
		 VALUES(1, 1, 'bad', 'bad@example.test', 'hash', -1, 1, 0)`); err == nil || !strings.Contains(err.Error(), "MAILBOX_PASSWORD_VERSION_IMMUTABLE") {
		t.Fatalf("expected restored guard to abort with MAILBOX_PASSWORD_VERSION_IMMUTABLE, got %v", err)
	}
}
