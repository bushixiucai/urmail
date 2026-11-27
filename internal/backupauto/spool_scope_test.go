package backupauto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSpoolGeneration plants one generation in dir with the given kinds and
// manifest scope; mail is written only when mail is true.
func writeSpoolGeneration(t *testing.T, dir, stamp, scope string, mail bool) {
	t.Helper()
	kinds := []struct{ kind, name string }{
		{KindPgDump, "urmail-" + stamp + ".dump"},
		{KindConfigTar, "urmail-config-" + stamp + ".tar.gz"},
	}
	if mail {
		kinds = append(kinds, struct{ kind, name string }{KindMailTar, "urmail-mail-" + stamp + ".tar.gz"})
	}
	artifacts := make([]Artifact, 0, len(kinds))
	for _, kind := range kinds {
		data := []byte("spool fixture " + kind.kind)
		if err := os.WriteFile(filepath.Join(dir, kind.name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		artifacts = append(artifacts, Artifact{Name: kind.name, Kind: kind.kind, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	}
	raw, err := json.Marshal(&Manifest{Version: 1, Scope: scope, Artifacts: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, ManifestName(stamp)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestReadSpoolRequiresMailForCompleteGenerations keeps the restore bar: a
// complete backup without the mail archive is refused.
func TestReadSpoolRequiresMailForCompleteGenerations(t *testing.T) {
	dir := t.TempDir()
	writeSpoolGeneration(t, dir, "20261119T140000Z", ScopeComplete, false)
	if _, _, err := ReadSpool(dir, "20261119T140000Z"); err == nil || !strings.Contains(err.Error(), "lacks the mail archive") {
		t.Fatalf("complete generation without mail: %v", err)
	}
	writeSpoolGeneration(t, dir, "20261119T140001Z", ScopeComplete, true)
	if _, artifacts, err := ReadSpool(dir, "20261119T140001Z"); err != nil || len(artifacts) != 3 {
		t.Fatalf("complete generation: artifacts=%d err=%v", len(artifacts), err)
	}
}

// TestReadSpoolAllowsMetadataOnlyMailOmission keeps the historical contract:
// a metadata-only generation omits the mail archive by design.
func TestReadSpoolAllowsMetadataOnlyMailOmission(t *testing.T) {
	dir := t.TempDir()
	writeSpoolGeneration(t, dir, "20261119T140002Z", "metadata-only", false)
	manifest, artifacts, err := ReadSpool(dir, "20261119T140002Z")
	if err != nil {
		t.Fatalf("metadata-only generation refused: %v", err)
	}
	if manifest == nil || manifest.Scope != "metadata-only" || len(artifacts) != 2 {
		t.Fatalf("metadata-only generation: manifest=%+v artifacts=%d", manifest, len(artifacts))
	}
}
