package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreStopsEveryWriterAndNeverRestartsOnFailure(t *testing.T) {
	var units []string
	err := stopRestoreWriters(context.Background(), func(_ context.Context, unit string) error {
		units = append(units, unit)
		if unit == "postfix.service" {
			return errors.New("injected stop failure")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "quarantine retained") {
		t.Fatal(err)
	}
	if len(units) != 15 || units[0] != "urmail-cert-renew.timer" || units[6] != "nginx.service" || units[9] != "php8.3-fpm.service" || units[13] != "urmaild.service" {
		t.Fatalf("unsafe stop order: %v", units)
	}
	if managedArchiveName("etc/urmail-roundcube/secrets.php") || managedArchiveName("etc/urmail-roundcube/pg-service.conf") || managedArchiveName("etc/urmail-roundcube/provision-state.json") {
		t.Fatal("source archive could overwrite target Roundcube credentials")
	}
	if managedArchiveName("var/lib/urmail-restore-quarantine/state.json") {
		t.Fatal("backup could overwrite independent quarantine")
	}
	if managedArchiveName("var/lib/urmail-restore-released-anything/release.json") {
		t.Fatal("backup could overwrite released record")
	}
}

func TestRestoreQuarantineStateRootOnly(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("private root state is exercised in an isolated Linux container")
	}
	parent := t.TempDir()
	path := filepath.Join(parent, "marker")
	state, err := createRestoreQuarantine(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ID) != 32 || state.StartedAt.IsZero() {
		t.Fatal("missing restore identity")
	}
	info, err := os.Stat(filepath.Join(path, "state.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("unsafe state mode: %v %v", info, err)
	}
	if _, err = createRestoreQuarantine(path); err == nil {
		t.Fatal("a second restore overwrote the first quarantine")
	}
	if err = os.Symlink(path, filepath.Join(parent, "symlink")); err != nil {
		t.Fatal(err)
	}
	if err = validatePrivateBackupDirectory(filepath.Join(parent, "symlink")); err == nil {
		t.Fatal("symlinked quarantine accepted")
	}
}

func TestRestoreCannotReleaseBeforeReplayClassificationCompletes(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires isolated root Linux acceptance container")
	}
	parent := t.TempDir()
	path := filepath.Join(parent, "marker")
	state, err := createRestoreQuarantine(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = verifyRestoreCompletion(root, state.ID); err == nil {
		t.Fatal("in-progress restore could be released")
	}
	proof := restoreInputProof{state.ID, strings.Repeat("a", 64)}
	if err = writeRestoreProof(path, "input.json", proof); err != nil {
		t.Fatal(err)
	}
	if err = completeRestoreQuarantine(path, state.ID); err == nil {
		t.Fatal("missing database stage accepted")
	}
	if err = writeRestoreProof(path, "database-restored.json", proof); err != nil {
		t.Fatal(err)
	}
	if err = completeRestoreQuarantine(path, state.ID); err != nil {
		t.Fatal(err)
	}
	if err = verifyRestoreCompletion(root, state.ID); err != nil {
		t.Fatal(err)
	}
	if err = verifyRestoreCompletion(root, "another-restore"); err == nil {
		t.Fatal("completion for another restore was accepted")
	}
}
