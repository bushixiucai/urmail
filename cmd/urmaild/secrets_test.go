package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
)

func TestRuntimeSecretsFailClosedOutsideDevelopment(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.key")
	invalid := filepath.Join(dir, "invalid.key")
	valid := filepath.Join(dir, "valid.key")
	if err := os.WriteFile(invalid, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(valid, []byte("12345678901234567890123456789012"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{missing, invalid, ""} {
		if box, err := loadRuntimeSecrets(&config.Config{MasterKeyPath: path}); err == nil || box != nil {
			t.Fatalf("production accepted missing or invalid key: %s", path)
		}
	}
	if box, err := loadRuntimeSecrets(&config.Config{MasterKeyPath: valid}); err != nil || box == nil {
		t.Fatal("valid production key was rejected")
	}
	if box, err := loadRuntimeSecrets(&config.Config{MasterKeyPath: missing, DevMode: true}); err != nil || box != nil {
		t.Fatal("explicit development mode must support secret-free fixtures")
	}
}
