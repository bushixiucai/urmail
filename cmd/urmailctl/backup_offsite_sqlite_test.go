package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/bushixiucai/urmail/internal/backupauto"
)

// offsiteSQLiteFixture seeds a migrated single-engine database and the env the
// CLI loads, so offsite bookkeeping must reach backup_runs without any
// PostgreSQL DSN.
func offsiteSQLiteFixture(t *testing.T) {
	t.Helper()
	newSetupSQLite(t)
	t.Setenv("URMAIL_DATABASE_URL_PLATFORM", "")
}

// writeOffsiteManifest writes the manifest and its artifacts into dir.
func writeOffsiteManifest(t *testing.T, dir, stamp string, identity string) string {
	t.Helper()
	dbName, dbKind := "urmail-"+stamp+".dump", backupauto.KindPgDump
	entries := make([]backupauto.Artifact, 0, 3)
	for _, spec := range []struct{ name, kind, data string }{
		{dbName, dbKind, "database fixture bytes"},
		{"urmail-config-" + stamp + ".tar.gz", backupauto.KindConfigTar, "config fixture bytes"},
		{"urmail-mail-" + stamp + ".tar.gz", backupauto.KindMailTar, "mail fixture bytes"},
	} {
		path := filepath.Join(dir, spec.name)
		if err := os.WriteFile(path, []byte(spec.data), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(spec.data))
		entries = append(entries, backupauto.Artifact{
			Name: spec.name, Kind: spec.kind, Size: int64(len(spec.data)),
			SHA256: hex.EncodeToString(sum[:]), Mode: 0o600,
		})
	}
	manifest := &backupauto.Manifest{Version: 1, Scope: backupauto.ScopeComplete, CreatedAt: time.Now().UTC(), Artifacts: entries}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, backupauto.ManifestName(stamp))
	if err = os.WriteFile(manifestPath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return manifestPath
}

// TestBackupOffsiteSQLiteRecordsRun proves the manual offsite upload records
// its run on the single engine: no PostgreSQL DSN exists there, so the ledger
// pool must come from the SQLite file. The successful invocation uses the
// "offsite" spelling and the failing one "upload", which also pins that both
// routes reach the same contract.
func TestBackupOffsiteSQLiteRecordsRun(t *testing.T) {
	offsiteSQLiteFixture(t)
	server := fakeS3Server(t)
	t.Setenv("URMAIL_BACKUP_S3_ACCESS_KEY_ID", "minioadmin")
	t.Setenv("URMAIL_BACKUP_S3_SECRET_KEY", "minioadmin")
	t.Setenv("URMAIL_BACKUP_S3_ENDPOINT", server.URL)

	old := backupOffsiteEffectiveUID
	backupOffsiteEffectiveUID = func() int { return 0 }
	t.Cleanup(func() { backupOffsiteEffectiveUID = old })

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	manifestPath := writeOffsiteManifest(t, dir, "20261119T120000Z", identity.Recipient().String())

	ctx := context.Background()
	if err = runBackup(ctx, []string{"offsite", "--manifest", manifestPath, "--uri", "s3://manual-bucket/prefix", "--recipient", identity.Recipient().String()}); err != nil {
		t.Fatalf("offsite upload on sqlite: %v", err)
	}
	pool, closePool, err := openPlatformPool(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer closePool()
	var state, manifestName string
	if err = pool.QueryRow(ctx, "SELECT state,manifest_name FROM backup_runs ORDER BY id DESC LIMIT 1").Scan(&state, &manifestName); err != nil {
		t.Fatal(err)
	}
	if state != backupauto.StateSuccess || manifestName == "" {
		t.Fatalf("sqlite success run state=%q manifest=%q", state, manifestName)
	}

	// The failing endpoint must record a failed run, not a success.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	t.Setenv("URMAIL_BACKUP_S3_ENDPOINT", failing.URL)
	if err = runBackup(ctx, []string{"upload", "--manifest", manifestPath, "--uri", "s3://manual-bucket/prefix", "--recipient", identity.Recipient().String()}); err == nil {
		t.Fatal("upload against a failing endpoint must fail")
	}
	if err = pool.QueryRow(ctx, "SELECT state FROM backup_runs ORDER BY id DESC LIMIT 1").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != backupauto.StateFailed {
		t.Fatalf("failed run recorded as %q", state)
	}
}
