package agentd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseOfflineManifestVerification(t *testing.T) {
	m := testManager(t)
	m.prepareOnly = true
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	loaded, digest, err := loadReleaseManager(m.cfg, m.root, m.targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.status.Ready || loaded.status.Step != "prepared" || len(digest) != 64 {
		t.Fatalf("invalid offline evidence: %+v %s", loaded.status, digest)
	}
	for path := range m.manifestFiles {
		if err := os.WriteFile(m.target(path), []byte("drift"), 0600); err != nil {
			t.Fatal(err)
		}
		break
	}
	if _, _, err := loadReleaseManager(m.cfg, m.root, m.targetRoot); err == nil {
		t.Fatal("accepted managed file drift")
	}
}

func TestReleaseOfflineRefusesPendingJournal(t *testing.T) {
	m := testManager(t)
	m.prepareOnly = true
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.root, "pending.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadReleaseManager(m.cfg, m.root, m.targetRoot); err == nil {
		t.Fatal("accepted pending journal")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("inspection mutated pending journal")
	}
}
