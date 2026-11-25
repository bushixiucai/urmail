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

type fixtureArtifact struct {
	name string
	data string
	kind string
}

func writeGeneration(t *testing.T, dir, stamp string, arts []fixtureArtifact) string {
	t.Helper()
	manifest := map[string]any{"version": 2, "scope": backupauto.ScopeComplete, "consistent": true}
	list := make([]backupauto.Artifact, 0, len(arts))
	for _, a := range arts {
		if err := os.WriteFile(filepath.Join(dir, a.name), []byte(a.data), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(a.data))
		list = append(list, backupauto.Artifact{Name: a.name, Kind: a.kind, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(a.data)), Mode: 0o600})
	}
	manifest["artifacts"] = list
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	name := "urmail-" + stamp + ".manifest.json"
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func completeArts(stamp string) []fixtureArtifact {
	return []fixtureArtifact{
		{name: "urmail-" + stamp + ".dump", data: "dump", kind: backupauto.KindPgDump},
		{name: "urmail-config-" + stamp + ".tar.gz", data: "config", kind: backupauto.KindConfigTar},
		{name: "urmail-mail-" + stamp + ".tar.gz", data: "mail", kind: backupauto.KindMailTar},
	}
}

func TestVerifyBackupArtifactsAcceptsIntactGeneration(t *testing.T) {
	dir := t.TempDir()
	name := writeGeneration(t, dir, "20260101T000000Z", completeArts("20260101T000000Z"))
	if err := verifyBackupArtifacts(dir, name); err != nil {
		t.Fatalf("intact generation rejected: %v", err)
	}
}

func TestVerifyBackupArtifactsRejectsTamperedArtifact(t *testing.T) {
	dir := t.TempDir()
	stamp := "20260101T000000Z"
	name := writeGeneration(t, dir, stamp, completeArts(stamp))
	if err := os.WriteFile(filepath.Join(dir, "urmail-mail-"+stamp+".tar.gz"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := verifyBackupArtifacts(dir, name)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("tampered artifact err=%v, want sha256 mismatch", err)
	}
}

func TestVerifyBackupArtifactsRejectsMissingMailArchive(t *testing.T) {
	dir := t.TempDir()
	stamp := "20260101T000000Z"
	arts := completeArts(stamp)[:2] // dump + config, all hashes valid
	name := writeGeneration(t, dir, stamp, arts)
	err := verifyBackupArtifacts(dir, name)
	if err == nil || !strings.Contains(err.Error(), "mail_tar") {
		t.Fatalf("manifest missing the mail archive err=%v, want a mail_tar completeness error", err)
	}
}

func TestVerifyBackupArtifactsHandlesV3Manifests(t *testing.T) {
	stamp := "20260101T000000Z"
	dir := t.TempDir()
	arts := make([]backupauto.Artifact, 0, 3)
	for _, a := range completeArts(stamp) {
		if err := os.WriteFile(filepath.Join(dir, a.name), []byte(a.data), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(a.data))
		arts = append(arts, backupauto.Artifact{Name: a.name, Kind: a.kind, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(a.data))})
	}
	valid, _ := json.Marshal(map[string]any{"version": 3, "layout_version": 1,
		"slots": map[string]any{"database": map[string]any{}}, "artifacts": arts})
	name := "urmail-" + stamp + ".manifest.json"
	if err := os.WriteFile(filepath.Join(dir, name), valid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackupArtifacts(dir, name); err != nil {
		t.Fatalf("well-formed v3 generation rejected: %v", err)
	}
	malformed, _ := json.Marshal(map[string]any{"version": 3, "artifacts": arts})
	if err := os.WriteFile(filepath.Join(dir, name), malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackupArtifacts(dir, name); err == nil || !strings.Contains(err.Error(), "malformed v3") {
		t.Fatalf("malformed v3 err=%v, want rejection", err)
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
