package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

func offlineRecoveryFixture(t *testing.T, files map[string][]byte) (string, string, []byte) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	target := filepath.Join(root, "target")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	currentManifest, err := json.Marshal(applyManifest{Status: ApplyStatus{Applied: "interrupted", Ready: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "manifest.json"), currentManifest, 0600); err != nil {
		t.Fatal(err)
	}
	j := applyJournal{Version: ApplyJournalVersion, Files: map[string]savedFile{}, Previous: ApplyStatus{Applied: "prior"}, PreviousPaths: []string{"/etc/postfix/main.cf"}, PreviousDomains: []string{"g5.test"}, Reloaded: []string{"nginx"}}
	for path, prior := range files {
		full := filepath.Join(target, strings.TrimPrefix(path, "/"))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		current := append([]byte("interrupted:"), prior...)
		if err := os.WriteFile(full, current, 0644); err != nil {
			t.Fatal(err)
		}
		j.Files[path] = savedFile{Content: prior, Mode: 0644, Exists: true, UID: os.Getuid(), GID: os.Getgid()}
	}
	raw, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(state, "pending.json")
	if err := os.WriteFile(pending, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return state, target, raw
}

func TestOfflinePendingRecoverySuccessNoExternalCommands(t *testing.T) {
	state, target, _ := offlineRecoveryFixture(t, map[string][]byte{"/etc/postfix/main.cf": []byte("previous-config")})
	backup, err := recoverPendingJournalOfflineCore(t.Context(), state, target, writeFileAtomic)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(target, "etc/postfix/main.cf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "previous-config" {
		t.Fatalf("content=%q", got)
	}
	if _, err := os.Lstat(filepath.Join(state, "pending.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending retained: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(state, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest applyManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Status.Applied != "prior" || manifest.Status.Ready {
		t.Fatalf("previous manifest mismatch: %+v", manifest.Status)
	}
	if st, err := os.Stat(backup); err != nil || st.Mode().Perm() != 0700 {
		t.Fatalf("backup dir: %v", err)
	}
	if st, err := os.Stat(filepath.Join(backup, "snapshot.json")); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("snapshot: %v", err)
	}
}

func TestOfflinePendingRecoveryMalformedJournalPreserved(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	target := filepath.Join(root, "target")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(state, "pending.json")
	raw := []byte("not-json")
	if err := os.WriteFile(pending, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := recoverPendingJournalOfflineCore(t.Context(), state, target, writeFileAtomic)
	if err == nil {
		t.Fatal("malformed accepted")
	}
	got, e := os.ReadFile(pending)
	if e != nil || string(got) != string(raw) {
		t.Fatalf("pending changed: %v", e)
	}
}

func TestOfflinePendingRecoveryFailureRollsBackFilesAndKeepsJournal(t *testing.T) {
	state, target, raw := offlineRecoveryFixture(t, map[string][]byte{"/etc/postfix/a": []byte("old-a"), "/etc/postfix/b": []byte("old-b")})
	originalManifest, err := os.ReadFile(filepath.Join(state, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	writer := func(path string, data []byte, mode os.FileMode, roots []string) (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("injected write failure")
		}
		return writeFileAtomic(path, data, mode, roots)
	}
	backup, err := recoverPendingJournalOfflineCore(t.Context(), state, target, writer)
	if err == nil || !strings.Contains(err.Error(), "original files restored") {
		t.Fatalf("expected rolled-back failure, got %v", err)
	}
	for p, want := range map[string]string{"/etc/postfix/a": "interrupted:old-a", "/etc/postfix/b": "interrupted:old-b"} {
		got, e := os.ReadFile(filepath.Join(target, strings.TrimPrefix(p, "/")))
		if e != nil || string(got) != want {
			t.Fatalf("%s changed: %v", p, e)
		}
	}
	got, e := os.ReadFile(filepath.Join(state, "pending.json"))
	if e != nil || string(got) != string(raw) {
		t.Fatalf("journal changed: %v", e)
	}
	manifest, e := os.ReadFile(filepath.Join(state, "manifest.json"))
	if e != nil || string(manifest) != string(originalManifest) {
		t.Fatalf("manifest changed on restore failure: %v", e)
	}
	if st, e := os.Stat(backup); e != nil || st.Mode().Perm() != 0700 {
		t.Fatalf("private backup invalid: %v", e)
	}
}

func TestOfflinePendingRecoveryManifestFailureRollsBack(t *testing.T) {
	state, target, raw := offlineRecoveryFixture(t, map[string][]byte{"/etc/postfix/main.cf": []byte("old")})
	beforeManifest, err := os.ReadFile(filepath.Join(state, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	backup, err := recoverPendingJournalOfflineCoreWithHooks(t.Context(), state, target, offlineRecoveryHooks{
		fileWriter:     writeFileAtomic,
		manifestWriter: func(*ApplyManager) error { return errors.New("injected manifest write failure") },
	})
	if err == nil || !strings.Contains(err.Error(), "original files restored") {
		t.Fatalf("expected manifest failure rollback, got %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(state, "manifest.json"))
	if err != nil || string(manifest) != string(beforeManifest) {
		t.Fatalf("manifest not restored: %v", err)
	}
	pending, err := os.ReadFile(filepath.Join(state, "pending.json"))
	if err != nil || string(pending) != string(raw) {
		t.Fatalf("pending not retained: %v", err)
	}
	file, err := os.ReadFile(filepath.Join(target, "etc/postfix/main.cf"))
	if err != nil || string(file) != "interrupted:old" {
		t.Fatalf("file not rolled back: %v", err)
	}
	if st, err := os.Stat(filepath.Join(backup, "snapshot.json")); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("private snapshot missing: %v", err)
	}
}

func TestOfflinePendingRecoveryPendingDeleteFailureReportsCommittedManifest(t *testing.T) {
	state, target, raw := offlineRecoveryFixture(t, map[string][]byte{"/etc/postfix/main.cf": []byte("old")})
	backup, err := recoverPendingJournalOfflineCoreWithHooks(t.Context(), state, target, offlineRecoveryHooks{
		fileWriter:     writeFileAtomic,
		manifestWriter: func(m *ApplyManager) error { return m.saveCurrentManifest() },
		pendingRemover: func(string) error { return errors.New("injected pending deletion failure") },
	})
	if err == nil || !strings.Contains(err.Error(), "manifest committed; pending journal remains unchanged") {
		t.Fatalf("expected explicit post-commit deletion failure, got %v", err)
	}
	pending, err := os.ReadFile(filepath.Join(state, "pending.json"))
	if err != nil || string(pending) != string(raw) {
		t.Fatalf("pending journal not preserved: %v", err)
	}
	manifestRaw, err := os.ReadFile(filepath.Join(state, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest applyManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Status.Applied != "prior" || manifest.Status.Step != "recovered" || manifest.Status.Ready {
		t.Fatalf("committed previous manifest mismatch: %+v", manifest.Status)
	}
	if st, err := os.Stat(filepath.Join(backup, "snapshot.json")); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("private snapshot missing: %v", err)
	}
}

func TestOfflinePendingRecoveryRejectsDNSJournal(t *testing.T) {
	state, target, raw := offlineRecoveryFixture(t, map[string][]byte{"/etc/postfix/main.cf": []byte("old")})
	var j applyJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	j.DNS = map[string]DNSRollbackItem{"g5.test": {Domain: "g5.test"}}
	bad, _ := json.Marshal(j)
	if err := os.WriteFile(filepath.Join(state, "pending.json"), bad, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := recoverPendingJournalOfflineCore(t.Context(), state, target, writeFileAtomic)
	if err == nil || !strings.Contains(err.Error(), "refuses DNS journal") {
		t.Fatalf("DNS not rejected: %v", err)
	}
	got, e := os.ReadFile(filepath.Join(state, "pending.json"))
	if e != nil || string(got) != string(bad) {
		t.Fatal("DNS journal changed")
	}
}

func writeSyntheticHoldSentinel(t *testing.T, state string) (string, []byte) {
	t.Helper()
	dir := filepath.Join(filepath.Dir(state), "maintenance")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, maintenance.DefaultUpgradeHoldFile)
	content := []byte("synthetic active hold sentinel\n")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	return path, content
}

func assertLargeFileUnchanged(t *testing.T, path string, wantSize int64, wantPrefix string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Size() != wantSize {
		t.Fatalf("file size changed: got %v, err=%v; want %d", info, err, wantSize)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := make([]byte, len(wantPrefix))
	if _, err := f.Read(got); err != nil || string(got) != wantPrefix {
		t.Fatalf("file prefix changed: got %q, err=%v", got, err)
	}
}

func TestOfflineRecoverySnapshotSerializedSizeLimitBoundary(t *testing.T) {
	snapshot := offlineRecoverySnapshot{Version: 1, Files: map[string]savedFile{"marker": {Content: []byte("small"), Exists: true}}}
	want, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	got, err := marshalOfflineRecoverySnapshot(snapshot, int64(len(want)))
	if err != nil || string(got) != string(want) {
		t.Fatalf("snapshot at exact encoded limit rejected: %v", err)
	}
	if _, err := marshalOfflineRecoverySnapshot(snapshot, int64(len(want)-1)); err == nil || !strings.Contains(err.Error(), "rollback reader limit") {
		t.Fatalf("snapshot over encoded limit accepted: %v", err)
	}
}

func TestOfflineRecoverySnapshotBoundedFileReadBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded")
	if err := os.WriteFile(path, []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := saveFileBounded(path, 4)
	if err != nil || string(got.Content) != "1234" {
		t.Fatalf("file at byte budget rejected: %v", err)
	}
	if _, err := saveFileBounded(path, 3); err == nil {
		t.Fatal("file over byte budget accepted")
	}
}

func TestOfflineRecoverySnapshotRejectsOversizedAppliedManifest(t *testing.T) {
	state, target, raw := offlineRecoveryFixture(t, map[string][]byte{"/etc/postfix/main.cf": []byte("old")})
	holdPath, holdRaw := writeSyntheticHoldSentinel(t, state)
	configPath := filepath.Join(target, "etc/postfix/main.cf")
	beforeConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeManifest, err := os.ReadFile(filepath.Join(state, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	backupManifestPath := filepath.Join(target, strings.TrimPrefix(appliedManifestBackupPath, string(filepath.Separator)))
	if err := os.MkdirAll(filepath.Dir(backupManifestPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupManifestPath, []byte("existing backup manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	oversize := int64(maxOfflineRecoverySnapshotContentBytes + 1)
	if err := os.Truncate(backupManifestPath, oversize); err != nil {
		t.Fatal(err)
	}
	_, err = recoverPendingJournalOfflineCore(t.Context(), state, target, writeFileAtomic)
	if err == nil || !strings.Contains(err.Error(), "remaining content budget") {
		t.Fatalf("oversized applied manifest accepted: %v", err)
	}
	assertLargeFileUnchanged(t, backupManifestPath, oversize, "existing backup manifest")
	config, err := os.ReadFile(configPath)
	if err != nil || string(config) != string(beforeConfig) {
		t.Fatalf("managed target changed before snapshot rejection: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(state, "manifest.json"))
	if err != nil || string(manifest) != string(beforeManifest) {
		t.Fatalf("current manifest changed before snapshot rejection: %v", err)
	}
	pending, err := os.ReadFile(filepath.Join(state, "pending.json"))
	if err != nil || string(pending) != string(raw) {
		t.Fatalf("pending journal changed before snapshot rejection: %v", err)
	}
	hold, err := os.ReadFile(holdPath)
	if err != nil || string(hold) != string(holdRaw) {
		t.Fatalf("hold sentinel changed before snapshot rejection: %v", err)
	}
}

func TestOfflineRecoverySnapshotRejectsCumulativeFiles(t *testing.T) {
	state, target, raw := offlineRecoveryFixture(t, map[string][]byte{"/etc/postfix/a": []byte("old-a"), "/etc/postfix/b": []byte("old-b")})
	holdPath, holdRaw := writeSyntheticHoldSentinel(t, state)
	perFile := int64(maxOfflineRecoverySnapshotContentBytes/2 + 1)
	for _, path := range []string{"/etc/postfix/a", "/etc/postfix/b"} {
		full := filepath.Join(target, strings.TrimPrefix(path, string(filepath.Separator)))
		if err := os.Truncate(full, perFile); err != nil {
			t.Fatal(err)
		}
	}
	_, err := recoverPendingJournalOfflineCore(t.Context(), state, target, writeFileAtomic)
	if err == nil || !strings.Contains(err.Error(), "remaining content budget") {
		t.Fatalf("cumulative snapshot content over limit accepted: %v", err)
	}
	for path, prefix := range map[string]string{"/etc/postfix/a": "interrupted:old-a", "/etc/postfix/b": "interrupted:old-b"} {
		assertLargeFileUnchanged(t, filepath.Join(target, strings.TrimPrefix(path, string(filepath.Separator))), perFile, prefix)
	}
	pending, err := os.ReadFile(filepath.Join(state, "pending.json"))
	if err != nil || string(pending) != string(raw) {
		t.Fatalf("pending journal changed after cumulative-limit rejection: %v", err)
	}
	hold, err := os.ReadFile(holdPath)
	if err != nil || string(hold) != string(holdRaw) {
		t.Fatalf("hold sentinel changed after cumulative-limit rejection: %v", err)
	}
}

func TestOfflineRecoveryLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := maintenance.AcquireLock(dir, maintenance.DefaultLockName)
	if err != nil {
		t.Fatal(err)
	}
	defer maintenance.ReleaseLock(first)
	ctx, cancel := context.WithTimeout(t.Context(), 0)
	defer cancel()
	if _, err := acquireOfflineRecoveryLock(ctx, dir); err == nil {
		t.Fatal("second lock acquired")
	}
}
