package datamigrate

import (
	"path/filepath"
	"testing"
)

// absTestPath resolves a repository-relative fixture path for tests: the
// loader accepts only clean absolute paths, exactly like the CLI callers.
func absTestPath(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
