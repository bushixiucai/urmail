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
}

func newFencePreflightFixture(t *testing.T) *fencePreflightFixture {
	t.Helper()
	allowUpgradeTestUser(t)
	dsn := testdb.New(t)
	testdb.MigrateTo(t, dsn, 159)
	platform, err := db.OpenPlatform(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(platform.Close)
	f := &fencePreflightFixture{platform: platform, maintDir: t.TempDir(), dropInDir: t.TempDir()}
	envFile := filepath.Join(f.maintDir, "urmail.env")
	if err = os.WriteFile(envFile, []byte("URMAIL_DATABASE_URL="+dsn+"\nURMAIL_DATABASE_URL_PLATFORM="+dsn+"\nURMAIL_QUOTE_HMAC_KEY=test-key\nURMAIL_POLICY_ENABLED=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("URMAIL_ENV_FILE", envFile)
	t.Setenv("URMAIL_MAINTENANCE_ROOT", f.maintDir)
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
	if _, err := f.platform.Exec(context.Background(),
		"INSERT INTO backup_runs(run_uid,state,scope,started_at,finished_at,error) VALUES($1,$2,$3,$4,statement_timestamp(),$5)",
		fmt.Sprintf("preflight-%s-%d", state, startedAgo.Milliseconds()), state, backupauto.ScopeComplete,
		time.Now().UTC().Add(-startedAgo), errorText); err != nil {
		t.Fatal(err)
	}
}

func (f *fencePreflightFixture) clearRuns(t *testing.T) {
	t.Helper()
	if _, err := f.platform.Exec(context.Background(), "DELETE FROM backup_runs"); err != nil {
		t.Fatal(err)
	}
}

func (f *fencePreflightFixture) fence(ctx context.Context, extra ...string) error {
	args := append([]string{"fence", "--root-dir", f.maintDir, "--drop-in-dir", f.dropInDir}, extra...)
	return runUpgrade(ctx, args)
}

// TestUpgradeFencePreflightRefusesUnfreshBackups is the R1 refusal matrix:
// no run at all, a failed run, a mid-flight run, and a success older than
// the window each refuse the fence BEFORE any lock, guard or hold is
// written.
func TestUpgradeFencePreflightRefusesUnfreshBackups(t *testing.T) {
	f := newFencePreflightFixture(t)
	ctx := context.Background()

	phases := []struct {
		name  string
		state string
		ago   time.Duration
	}{
		{"no runs yet", "", 0},
		{"failed run", backupauto.StateFailed, time.Hour},
		{"running run", backupauto.StateRunning, 10 * time.Minute},
		{"success outside window", backupauto.StateSuccess, 25 * time.Hour},
	}
	for _, phase := range phases {
		t.Run(phase.name, func(t *testing.T) {
			f.clearRuns(t)
			if phase.state != "" {
				f.seedRun(t, phase.state, phase.ago, "upload denied by destination")
			}
			err := f.fence(ctx)
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
		})
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

// TestManualOffsiteUploadRecordsRun is the R1 landing contract: the manual
// "backup offsite upload" records a backup_runs row -- success only after
// the uploaded generation re-verifies remotely -- so the fence pre-flight
// and the release gate see the same truth as the scheduled worker.
func TestManualOffsiteUploadRecordsRun(t *testing.T) {
	dsn := testdb.New(t)
	testdb.MigrateTo(t, dsn, 159)
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
