package store

import (
	"regexp"
	"strings"
	"testing"
)

// TestMarketingFlowAuditVariantsAgree keeps the PostgreSQL twin of the
// marketing-flow audit insert honest: the PG runtime uses a SECURITY DEFINER
// procedure, so the Go constant is not executed in production, but it documents
// the exact statement the SQLite dialect substitutes. The two variants must
// write the same columns with the same literal values and the same argument
// count; only the placeholder syntax differs. Without this comparison the
// constant would rot silently next to a schema change.
func TestMarketingFlowAuditVariantsAgree(t *testing.T) {
	columns := func(statement string) ([]string, []string) {
		t.Helper()
		open := strings.Index(statement, "(")
		closeIdx := strings.Index(statement, ")")
		if open < 0 || closeIdx < open {
			t.Fatalf("statement has no column list: %q", statement)
		}
		cols := strings.Split(statement[open+1:closeIdx], ",")
		for i := range cols {
			cols[i] = strings.TrimSpace(cols[i])
		}
		valuesAt := strings.Index(statement, "VALUES(")
		if valuesAt < 0 {
			t.Fatalf("statement has no VALUES list: %q", statement)
		}
		values := strings.Split(statement[valuesAt+len("VALUES("):strings.LastIndex(statement, ")")], ",")
		for i := range values {
			values[i] = strings.TrimSpace(values[i])
		}
		return cols, values
	}
	sqliteCols, sqliteValues := columns(marketingFlowAuditInsertSQL)
	pgCols, pgValues := columns(marketingFlowAuditInsertPGSQL)
	if strings.Join(sqliteCols, ",") != strings.Join(pgCols, ",") {
		t.Fatalf("column lists differ: sqlite=%v pg=%v", sqliteCols, pgCols)
	}
	if len(sqliteValues) != len(pgValues) {
		t.Fatalf("value count differs: sqlite=%v pg=%v", sqliteValues, pgValues)
	}
	sqlitePlaceholder := regexp.MustCompile(`^\?\d+$`)
	pgPlaceholder := regexp.MustCompile(`^\$\d+$`)
	for i := range sqliteValues {
		switch {
		case sqliteValues[i] == pgValues[i]:
			if strings.HasPrefix(sqliteValues[i], "?") || strings.HasPrefix(sqliteValues[i], "$") {
				t.Fatalf("literal expected at %d, both sides carry a placeholder: %q", i, sqliteValues[i])
			}
		case sqlitePlaceholder.MatchString(sqliteValues[i]) && pgPlaceholder.MatchString(pgValues[i]):
			if strings.TrimLeft(sqliteValues[i], "?") != strings.TrimLeft(pgValues[i], "$") {
				t.Fatalf("placeholder %d numbering differs: sqlite=%q pg=%q", i, sqliteValues[i], pgValues[i])
			}
		default:
			t.Fatalf("variant mismatch at value %d: sqlite=%q pg=%q", i, sqliteValues[i], pgValues[i])
		}
	}
}
