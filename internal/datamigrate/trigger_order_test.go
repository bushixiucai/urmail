package datamigrate

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
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
