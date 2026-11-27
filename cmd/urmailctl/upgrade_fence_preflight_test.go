package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/maintenance"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// fencePreflightFixture points the CLI's platform DSN at a real migrated
// fixture database so the upgrade-fence fresh-backup pre-flight reads
// backup_runs rows the test seeds.
type fencePreflightFixture struct {
	platform  *db.Pool
	maintDir  string
	dropInDir string
	envFile   string
	spool     string
}

func newFencePreflightFixture(t *testing.T) *fencePreflightFixture {
	t.Helper()
	allowUpgradeTestUser(t)
	dsn := testdb.New(t)
	testdb.MigrateTo(t, dsn, 160)
	platform, err := db.OpenPlatform(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(platform.Close)
	f := &fencePreflightFixture{platform: platform, maintDir: t.TempDir(), dropInDir: t.TempDir(), spool: t.TempDir()}
	f.envFile = filepath.Join(f.maintDir, "urmail.env")
	if err = os.WriteFile(f.envFile, []byte("URMAIL_DATABASE_URL="+dsn+"\nURMAIL_DATABASE_URL_PLATFORM="+dsn+"\nURMAIL_QUOTE_HMAC_KEY=test-key\nURMAIL_POLICY_ENABLED=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("URMAIL_ENV_FILE", f.envFile)
	t.Setenv("URMAIL_MAINTENANCE_ROOT", f.maintDir)
	// The pre-flight verifies the newest generation on disk, so the fixture
	// owns a private spool instead of the fixed production directory.
	t.Setenv("URMAIL_BACKUP_SPOOL", f.spool)
	// Every writer unit reports inactive so the fence completes without
	// exercising systemd, isolating the pre-flight decision under test.
	reset := setBackupUnitActionForTest(func(_ context.Context, action, unit string) (bool, error) {
		if action == "state" {
			return false, nil
		}
		return false, fmt.Errorf("unexpected unit action %s %s", action, unit)
	})
	t.Cleanup(reset)
	return f
}

func (f *fencePreflightFixture) seedRun(t *testing.T, state string, startedAgo time.Duration, errorText string) {
	t.Helper()
	f.seedRunScoped(t, state, backupauto.ScopeComplete, startedAgo, errorText, backupauto.ManifestName(backupauto.StampFor(time.Now().UTC().Add(-startedAgo))))
}

// seedRunScoped plants one finished run with explicit scope and manifest
// name so the pre-flight can be probed against metadata-only and
// stale-stamp generations (R1 QA).
func (f *fencePreflightFixture) seedRunScoped(t *testing.T, state, scope string, startedAgo time.Duration, errorText, manifestName string) {
	t.Helper()
	f.seedRunOffsite(t, state, scope, startedAgo, errorText, manifestName, backupauto.OffsiteStateUploaded, "s3://fixture/backup.dump")
}

// seedRunOffsite plants a finished run with an explicit offsite disposition.
func (f *fencePreflightFixture) seedRunOffsite(t *testing.T, state, scope string, startedAgo time.Duration, errorText, manifestName, offsiteState, offsiteURI string) {
	t.Helper()
	if _, err := f.platform.Exec(context.Background(),
		"INSERT INTO backup_runs(run_uid,state,scope,started_at,finished_at,error,manifest_name,offsite_state,offsite_uri) VALUES($1,$2,$3,$4,statement_timestamp(),$5,$6,$7,$8)",
		fmt.Sprintf("pf-%d-%s", startedAgo.Milliseconds(), offsiteState), state, scope,
		time.Now().UTC().Add(-startedAgo), errorText, manifestName, offsiteState, offsiteURI); err != nil {
		t.Fatal(err)
	}
	if state == backupauto.StateSuccess && scope == backupauto.ScopeComplete {
		plantCompleteGeneration(t, f.spool, manifestName, backupauto.KindPgDump)
	}
}

// plantCompleteGeneration writes one complete generation (artifacts plus
// manifest) so the pre-flight's fail-closed integrity check has real files;
// the run row alone is metadata and is not accepted.
func plantCompleteGeneration(t *testing.T, spool, manifestName, dbKind string) {
	t.Helper()
	stamp := strings.TrimSuffix(strings.TrimPrefix(manifestName, "urmail-"), ".manifest.json")
	dbName := "urmail-" + stamp + ".dump"
	if dbKind == backupauto.KindSqliteSnapshot {
		dbName = "urmail-" + stamp + ".sqlite"
	}
	specs := []struct{ name, kind, data string }{
		{dbName, dbKind, "database fixture bytes"},
		{"urmail-config-" + stamp + ".tar.gz", backupauto.KindConfigTar, "config fixture bytes"},
		{"urmail-mail-" + stamp + ".tar.gz", backupauto.KindMailTar, "mail fixture bytes"},
	}
	artifacts := make([]backupauto.Artifact, 0, len(specs))
	for _, spec := range specs {
		data := []byte(spec.data)
		if err := os.WriteFile(filepath.Join(spool, spec.name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		artifacts = append(artifacts, backupauto.Artifact{
			Name: spec.name, Kind: spec.kind, Size: int64(len(data)),
			SHA256: hex.EncodeToString(sum[:]), Mode: 0o600,
		})
	}
	manifest := &backupauto.Manifest{Version: 1, Scope: backupauto.ScopeComplete, CreatedAt: time.Now().UTC(), Artifacts: artifacts}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, manifestName), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fencePreflightFixture) clearRuns(t *testing.T) {
	t.Helper()
	if _, err := f.platform.Exec(context.Background(), "DELETE FROM backup_runs"); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeFencePreflightRefusesMissingGenerationArtifact proves the fence
// rejects a ledger row whose generation lost an artifact: the row is metadata,
// the files are the backup.
func TestUpgradeFencePreflightRefusesMissingGenerationArtifact(t *testing.T) {
	f := newFencePreflightFixture(t)
	startedAgo := time.Hour
	manifestName := backupauto.ManifestName(backupauto.StampFor(time.Now().UTC().Add(-startedAgo)))
	f.seedRunScoped(t, backupauto.StateSuccess, backupauto.ScopeComplete, startedAgo, "", manifestName)
	stamp := strings.TrimSuffix(strings.TrimPrefix(manifestName, "urmail-"), ".manifest.json")
	if err := os.Remove(filepath.Join(f.spool, "urmail-"+stamp+".dump")); err != nil {
		t.Fatal(err)
	}
	err := f.fence(context.Background())
	if err == nil {
		t.Fatal("a generation with a missing artifact established the fence")
	}
	t.Logf("fence refused missing artifact: %v", err)
	if !strings.Contains(err.Error(), "open backup artifact") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if maintenance.IsUpgradeHeld(f.maintDir) {
		t.Fatal("refused fence must not be established")
	}
}

// TestUpgradeFencePreflightRefusesTamperedGenerationArtifact proves a changed
// artifact byte is refused on the hash the manifest recorded.
func TestUpgradeFencePreflightRefusesTamperedGenerationArtifact(t *testing.T) {
	f := newFencePreflightFixture(t)
	startedAgo := time.Hour
	manifestName := backupauto.ManifestName(backupauto.StampFor(time.Now().UTC().Add(-startedAgo)))
	f.seedRunScoped(t, backupauto.StateSuccess, backupauto.ScopeComplete, startedAgo, "", manifestName)
	stamp := strings.TrimSuffix(strings.TrimPrefix(manifestName, "urmail-"), ".manifest.json")
	artifact := filepath.Join(f.spool, "urmail-"+stamp+".dump")
	raw, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(artifact, append(raw, []byte("tampered")...), 0o600); err != nil {
		t.Fatal(err)
	}
	err = f.fence(context.Background())
	if err == nil {
		t.Fatal("a generation with a tampered artifact established the fence")
	}
	t.Logf("fence refused tampered artifact: %v", err)
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if maintenance.IsUpgradeHeld(f.maintDir) {
		t.Fatal("refused fence must not be established")
	}
}

func (f *fencePreflightFixture) fence(ctx context.Context, extra ...string) error {
	args := append([]string{"fence", "--root-dir", f.maintDir, "--drop-in-dir", f.dropInDir}, extra...)
	return runUpgrade(ctx, args)
}

// TestUpgradeFencePreflightRefusesUnfreshBackups is the R1 refusal matrix:
// no run at all, a failed run, a mid-flight run, a success older than the
// window, a metadata-only success, and a freshly re-uploaded old
// generation each refuse the fence BEFORE any lock, guard or hold is
// written.
func TestUpgradeFencePreflightRefusesUnfreshBackups(t *testing.T) {
	f := newFencePreflightFixture(t)
	ctx := context.Background()

	// metadataRun seeds a fresh, successful, NON-complete generation.
	metadataRun := func(t *testing.T) {
		t.Helper()
		f.seedRunScoped(t, backupauto.StateSuccess, "metadata-only", time.Hour, "", backupauto.ManifestName(backupauto.StampFor(time.Now().UTC().Add(-time.Hour))))
	}
	// staleStampRun seeds a complete success whose run row is fresh but the
	// manifest stamp (the generation's own clock) is 25h old.
	staleStampRun := func(t *testing.T) {
		t.Helper()
		f.seedRunScoped(t, backupauto.StateSuccess, backupauto.ScopeComplete, time.Hour, "",
			backupauto.ManifestName(backupauto.StampFor(time.Now().UTC().Add(-25*time.Hour))))
	}

	t.Run("no runs yet", func(t *testing.T) {
		f.clearRuns(t)
		f.assertRefused(t)
	})
	t.Run("failed run", func(t *testing.T) {
		f.clearRuns(t)
		f.seedRun(t, backupauto.StateFailed, time.Hour, "upload denied by destination")
		f.assertRefused(t)
	})
	t.Run("running run", func(t *testing.T) {
		f.clearRuns(t)
		f.seedRun(t, backupauto.StateRunning, 10*time.Minute, "")
		f.assertRefused(t)
	})
	t.Run("success outside window", func(t *testing.T) {
		f.clearRuns(t)
		f.seedRun(t, backupauto.StateSuccess, 25*time.Hour, "")
		f.assertRefused(t)
	})
	t.Run("metadata-only success", func(t *testing.T) {
		f.clearRuns(t)
		metadataRun(t)
		err := f.fence(ctx)
		if err == nil || !strings.Contains(err.Error(), "fresh-backup pre-flight") || !strings.Contains(err.Error(), "complete") {
			t.Fatalf("metadata-only success must be refused as not a complete backup, got %v", err)
		}
		if maintenance.IsUpgradeHeld(f.maintDir) {
			t.Fatal("refused fence must not write the hold marker")
		}
	})
	t.Run("re-uploaded old generation", func(t *testing.T) {
		f.clearRuns(t)
		staleStampRun(t)
		err := f.fence(ctx)
		if err == nil || !strings.Contains(err.Error(), "fresh-backup pre-flight") || !strings.Contains(err.Error(), "old") {
			t.Fatalf("a fresh run row over a 25h-old generation must be refused, got %v", err)
		}
		if maintenance.IsUpgradeHeld(f.maintDir) {
			t.Fatal("refused fence must not write the hold marker")
		}
	})
}

// assertRefused expects the plain refusal shape: pre-flight named, nothing
// written.
func (f *fencePreflightFixture) assertRefused(t *testing.T) {
	t.Helper()
	err := f.fence(t.Context())
	if err == nil || !strings.Contains(err.Error(), "fresh-backup pre-flight") {
		t.Fatalf("fence must be refused with the pre-flight reason, got %v", err)
	}
	if maintenance.IsUpgradeHeld(f.maintDir) {
		t.Fatal("refused fence must not write the hold marker")
	}
	guards, guardErr := maintenance.CheckDropInGuards(f.dropInDir, maintenance.DropInGuardedUnits)
	if guardErr != nil {
		t.Fatal(guardErr)
	}
	for unit, held := range guards {
		if held {
			t.Fatalf("refused fence must not install guards, found %s", unit)
		}
	}
}

// TestUpgradeFencePreflightAllowsFreshSuccess proves a fresh successful
// offsite run lets the fence proceed and the fence is actually established.
func TestUpgradeFencePreflightAllowsFreshSuccess(t *testing.T) {
	f := newFencePreflightFixture(t)
	f.seedRun(t, backupauto.StateSuccess, time.Hour, "")
	if err := f.fence(t.Context()); err != nil {
		t.Fatalf("fence with a fresh successful backup must proceed: %v", err)
	}
	if !maintenance.IsUpgradeHeld(f.maintDir) {
		t.Fatal("expected fence to be established")
	}
}

// TestUpgradeFencePreflightCustomWindow honors --fresh-backup-window: an
// 8h window refuses a 9h-old success that the default 24h window accepts.
func TestUpgradeFencePreflightCustomWindow(t *testing.T) {
	f := newFencePreflightFixture(t)
	f.seedRun(t, backupauto.StateSuccess, 9*time.Hour, "")
	if err := f.fence(t.Context(), "--fresh-backup-window", "8h"); err == nil ||
		!strings.Contains(err.Error(), "fresh-backup pre-flight") {
		t.Fatalf("9h-old success must fail an 8h window, got %v", err)
	}
	if maintenance.IsUpgradeHeld(f.maintDir) {
		t.Fatal("refused fence must not write the hold marker")
	}
}

// TestUpgradeFencePreflightSkipRecordsOperatorOverride proves --skip-preflight
// fences anyway and the override is durably recorded in the fence reason.
func TestUpgradeFencePreflightSkipRecordsOperatorOverride(t *testing.T) {
	f := newFencePreflightFixture(t)
	if err := f.fence(t.Context(), "--skip-preflight"); err != nil {
		t.Fatalf("skip-preflight must allow fencing: %v", err)
	}
	if !maintenance.IsUpgradeHeld(f.maintDir) {
		t.Fatal("expected fence to be established")
	}
	status, err := maintenance.CheckFenceStatus(f.maintDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.Meta == nil || !strings.Contains(status.Meta.Reason, "[fresh-backup pre-flight skipped via --skip-preflight]") {
		t.Fatalf("override must be recorded in the fence reason, got %+v", status.Meta)
	}
}

// fakeS3Server is an in-memory S3-compatible endpoint for the manual
// upload test: PUT stores, GET replays, everything else 405s.
func fakeS3Server(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			objects[r.URL.Path] = body
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			mu.Lock()
			body, ok := objects[r.URL.Path]
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestUpgradeFencePreflightDegradesWhenPlatformDBUnreachable is R1 QA (a):
// when the platform database cannot be reached, the pre-flight must not
// block the fence (repairing the database is often the reason for the
// upgrade); the degradation is recorded in the fence reason instead.
func TestUpgradeFencePreflightDegradesWhenPlatformDBUnreachable(t *testing.T) {
	f := newFencePreflightFixture(t)
	// Point the CLI at a closed port: connection refused is immediate.
	unreachable := "postgres://platform:nopass@127.0.0.1:1/urmail?sslmode=disable"
	if err := os.WriteFile(f.envFile, []byte("URMAIL_DATABASE_URL="+unreachable+"\nURMAIL_DATABASE_URL_PLATFORM="+unreachable+"\nURMAIL_QUOTE_HMAC_KEY=test-key\nURMAIL_POLICY_ENABLED=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.fence(t.Context()); err != nil {
		t.Fatalf("unreachable platform DB must not block the fence: %v", err)
	}
	if !maintenance.IsUpgradeHeld(f.maintDir) {
		t.Fatal("expected fence to be established")
	}
	status, err := maintenance.CheckFenceStatus(f.maintDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.Meta == nil || !strings.Contains(status.Meta.Reason, "[preflight degraded: platform DB unreachable") {
		t.Fatalf("degradation must be recorded in the fence reason, got %+v", status.Meta)
	}
}

// TestManualOffsiteUploadRecordsRun is the R1 landing contract: the manual
// "backup offsite upload" records a backup_runs row -- success only after
// the uploaded generation re-verifies remotely -- so the fence pre-flight
// and the release gate see the same truth as the scheduled worker.
func TestManualOffsiteUploadRecordsRun(t *testing.T) {
	dsn := testdb.New(t)
	testdb.MigrateTo(t, dsn, 160)
	platform, err := db.OpenPlatform(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(platform.Close)

	old := backupOffsiteEffectiveUID
	backupOffsiteEffectiveUID = func() int { return 0 }
	t.Cleanup(func() { backupOffsiteEffectiveUID = old })

	envDir := t.TempDir()
	envFile := filepath.Join(envDir, "urmail.env")
	if err = os.WriteFile(envFile, []byte("URMAIL_DATABASE_URL="+dsn+"\nURMAIL_DATABASE_URL_PLATFORM="+dsn+"\nURMAIL_QUOTE_HMAC_KEY=test-key\nURMAIL_POLICY_ENABLED=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("URMAIL_ENV_FILE", envFile)

	server := fakeS3Server(t)
	t.Setenv("URMAIL_BACKUP_S3_ACCESS_KEY_ID", "minioadmin")
	t.Setenv("URMAIL_BACKUP_S3_SECRET_KEY", "minioadmin")
	t.Setenv("URMAIL_BACKUP_S3_ENDPOINT", server.URL)

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stamp := "20261119T120000Z"
	entries := make([]backupauto.Artifact, 0, 2)
	for _, spec := range []struct{ name, kind, data string }{
		{"urmail-" + stamp + ".dump", backupauto.KindPgDump, "pg dump fixture bytes"},
		{"urmail-config-" + stamp + ".tar.gz", backupauto.KindConfigTar, "config fixture bytes"},
	} {
		path := filepath.Join(dir, spec.name)
		if err = os.WriteFile(path, []byte(spec.data), 0o600); err != nil {
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

	ctx := t.Context()
	err = runBackup(ctx, []string{"upload", "--manifest", manifestPath, "--uri", "s3://manual-bucket/prefix", "--recipient", identity.Recipient().String()})
	if err != nil {
		t.Fatalf("manual offsite upload: %v", err)
	}
	var state, note, manifestName, uri string
	var bytes int64
	if err = platform.QueryRow(ctx,
		"SELECT state,COALESCE(error,''),manifest_name,offsite_uri,bytes FROM backup_runs ORDER BY id DESC LIMIT 1").
		Scan(&state, &note, &manifestName, &uri, &bytes); err != nil {
		t.Fatal(err)
	}
	if state != backupauto.StateSuccess || note != "" {
		t.Fatalf("manual upload must record a clean success run, got state=%q error=%q", state, note)
	}
	if manifestName != backupauto.ManifestName(stamp) || uri != "s3://manual-bucket/prefix" || bytes <= 0 {
		t.Fatalf("manual run row incomplete: manifest=%q uri=%q bytes=%d", manifestName, uri, bytes)
	}

	// A failed manual upload records a failed run with its cause, never a
	// success: the endpoint answers 500 to every PUT.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	if err = os.Setenv("URMAIL_BACKUP_S3_ENDPOINT", failing.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("URMAIL_BACKUP_S3_ENDPOINT") })
	if err = runBackup(ctx, []string{"upload", "--manifest", manifestPath, "--uri", "s3://manual-bucket/prefix", "--recipient", identity.Recipient().String()}); err == nil {
		t.Fatal("upload against a failing endpoint must fail")
	}
	var failedState, failedNote string
	if err = platform.QueryRow(ctx,
		"SELECT state,COALESCE(error,'') FROM backup_runs ORDER BY id DESC LIMIT 1").Scan(&failedState, &failedNote); err != nil {
		t.Fatal(err)
	}
	if failedState != backupauto.StateFailed || !strings.Contains(failedNote, "HTTP 500") {
		t.Fatalf("failed manual upload must record a failed run naming the cause, got state=%q error=%q", failedState, failedNote)
	}
	var runs int
	if err = platform.QueryRow(ctx, "SELECT count(*) FROM backup_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Fatalf("expected exactly two recorded manual runs, got %d", runs)
	}
}

// TestUpgradeFencePreflightOffsiteDisposition: an empty or invalid
// offsite_state is NOT satisfied; uploaded requires an offsite object; an
// approved local-only run passes and the verdict prints offsite=...
func TestUpgradeFencePreflightOffsiteDisposition(t *testing.T) {
	state := func(t *testing.T, offsiteState, offsiteURI string) *fencePreflightFixture {
		t.Helper()
		f := newFencePreflightFixture(t)
		f.seedRunOffsite(t, backupauto.StateSuccess, backupauto.ScopeComplete, time.Hour, "",
			backupauto.ManifestName(backupauto.StampFor(time.Now().UTC().Add(-time.Hour))), offsiteState, offsiteURI)
		return f
	}
	t.Run("historical empty state is not satisfied", func(t *testing.T) {
		f := state(t, "", "")
		if err := f.fence(context.Background()); err == nil || !strings.Contains(err.Error(), "offsite_state") {
			t.Fatalf("empty offsite_state must be refused, got %v", err)
		}
	})
	// Invalid values cannot be written: migration 0160 carries a CHECK
	// constraint. The gate's default branch stays as defense in depth for a
	// row that predates or bypasses it; it is not seedable through SQL.
	t.Run("uploaded without an offsite uri is refused", func(t *testing.T) {
		f := state(t, backupauto.OffsiteStateUploaded, "")
		if err := f.fence(context.Background()); err == nil || !strings.Contains(err.Error(), "offsite_uri") {
			t.Fatalf("uploaded without uri must be refused, got %v", err)
		}
	})
	t.Run("uploaded with an offsite uri passes", func(t *testing.T) {
		f := state(t, backupauto.OffsiteStateUploaded, "s3://fixture/backup.dump")
		if err := f.fence(context.Background()); err != nil {
			t.Fatalf("uploaded run must pass, got %v", err)
		}
	})
	t.Run("pending local only passes and is printed", func(t *testing.T) {
		f := state(t, backupauto.OffsiteStatePendingLocalOnly, "")
		out := captureStdoutErr(t, func() error { return f.fence(context.Background()) })
		if !strings.Contains(out, "offsite="+backupauto.OffsiteStatePendingLocalOnly) {
			t.Fatalf("pre-flight must print the reduced offsite口径: %s", out)
		}
	})
}

func captureStdoutErr(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = old }()
	runErr := fn()
	_ = writer.Close()
	out, readErr := io.ReadAll(reader)
	if runErr != nil {
		t.Fatalf("fence: %v", runErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(out)
}
