package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func secureTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "anchor-test-")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return abs
}

func TestAnchorFileStructuralRejections(t *testing.T) {
	dir := secureTempDir(t)
	key := filepath.Join(dir, "anchor.pub")
	if err := os.WriteFile(key, []byte("00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAnchorFile(key, false); err == nil {
		t.Fatal("non-key anchor accepted")
	}
	// Writable anchor is refused even in the structural test mode.
	if err := os.Chmod(key, 0o622); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAnchorFile(key, false); err == nil || !contains(err.Error(), "world writable") {
		t.Fatalf("writable anchor accepted: %v", err)
	}
	_ = os.Chmod(key, 0o600)
	// Symlinked ancestor is refused.
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAnchorFile(filepath.Join(linkDir, "missing.pub"), false); err == nil {
		t.Fatal("symlinked ancestor accepted")
	}
	// Multiple hard links are refused.
	second := filepath.Join(dir, "second.pub")
	if err := os.Link(key, second); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAnchorFile(key, false); err == nil || !contains(err.Error(), "one link") {
		t.Fatalf("multi-link anchor accepted: %v", err)
	}
	// Production ownership check rejects a non-root anchor.
	if os.Geteuid() != 0 {
		if _, err := loadAnchorFile(key, true); err == nil {
			t.Fatal("non-production anchor accepted for the root-owned product path")
		}
	}
	var _ syscall.Stat_t
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestAnchorRejectsSpecialPermissionBits proves setuid/setgid/sticky anchor
// modes are refused rather than accepted as ordinary ownership.
func TestAnchorRejectsSpecialPermissionBits(t *testing.T) {
	dir := secureTempDir(t)
	key := filepath.Join(dir, "anchor.pub")
	if err := os.WriteFile(key, []byte("00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Chmod(key, 0o4600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Chmod(key, 0o600) })
	if _, err := loadAnchorFile(key, false); err == nil || !contains(err.Error(), "special permission bits") {
		t.Fatalf("setuid anchor accepted: %v", err)
	}
}
