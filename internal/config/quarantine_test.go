package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreQuarantineFailsClosed(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "marker")
	if restoreQuarantinePresent(marker) {
		t.Fatal("absent marker quarantined")
	}
	if err := os.Mkdir(marker, 0o700); err != nil {
		t.Fatal(err)
	}
	if !restoreQuarantinePresent(marker) {
		t.Fatal("existing marker ignored")
	}
	// A dangling symlink or invalid ancestor must not disable quarantine.
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	if !restoreQuarantinePresent(dangling) {
		t.Fatal("dangling marker ignored")
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !restoreQuarantinePresent(filepath.Join(file, "invalid")) {
		t.Fatal("unreadable marker failed open")
	}
}
