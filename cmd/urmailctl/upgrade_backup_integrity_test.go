package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/backupauto"
)

func writeManifest(t *testing.T, dir, stamp, data string) string {
	t.Helper()
	name := "urmail-" + stamp + ".sqlite"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(data))
	manifest := map[string]any{
		"version": 2, "scope": backupauto.ScopeComplete, "consistent": true,
		"artifacts": []backupauto.Artifact{{Name: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), Mode: 0o600}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestName := "urmail-" + stamp + ".manifest.json"
	if err := os.WriteFile(filepath.Join(dir, manifestName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return manifestName
}

func TestVerifyBackupArtifactsAcceptsIntactGeneration(t *testing.T) {
	dir := t.TempDir()
	name := writeManifest(t, dir, "20260101T000000Z", "intact")
	if err := verifyBackupArtifacts(dir, name); err != nil {
		t.Fatalf("intact generation rejected: %v", err)
	}
}

func TestVerifyBackupArtifactsRejectsTamperedArtifact(t *testing.T) {
	dir := t.TempDir()
	name := writeManifest(t, dir, "20260101T000000Z", "intact")
	if err := os.WriteFile(filepath.Join(dir, "urmail-20260101T000000Z.sqlite"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := verifyBackupArtifacts(dir, name)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("tampered artifact err=%v, want sha256 mismatch", err)
	}
}

func TestVerifyBackupArtifactsRejectsMissingArtifact(t *testing.T) {
	dir := t.TempDir()
	name := writeManifest(t, dir, "20260101T000000Z", "intact")
	if err := os.Remove(filepath.Join(dir, "urmail-20260101T000000Z.sqlite")); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackupArtifacts(dir, name); err == nil {
		t.Fatal("missing artifact was accepted")
	}
}

func TestVerifyFreeSpaceHonorsConfiguredMinimum(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("URMAIL_UPGRADE_MIN_FREE_BYTES", "0")
	if err := verifyFreeSpace(dir); err != nil {
		t.Fatalf("zero minimum rejected: %v", err)
	}
	t.Setenv("URMAIL_UPGRADE_MIN_FREE_BYTES", "9223372036854775807")
	if err := verifyFreeSpace(dir); err == nil {
		t.Fatal("unreachable free-space minimum was accepted")
	}
	t.Setenv("URMAIL_UPGRADE_MIN_FREE_BYTES", "not-a-number")
	if err := verifyFreeSpace(dir); err == nil {
		t.Fatal("invalid minimum was accepted")
	}
}
