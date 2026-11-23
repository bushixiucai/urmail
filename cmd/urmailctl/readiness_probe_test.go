package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/maintenance"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func readinessFence(t *testing.T, generation uint64) string {
	t.Helper()
	root := t.TempDir()
	meta := maintenance.UpgradeFenceMeta{Version: 2, Generation: generation, CreatedAt: time.Now().UTC(), Reason: "readiness probe fixture", Operator: "fixture", Units: []string{"postfix.service"}}
	if err := maintenance.CreateUpgradeFence(root, meta); err != nil {
		t.Fatalf("create fence: %v", err)
	}
	return root
}

// probeRoleDSN creates a SELECT-only login role for schema_migrations and
// returns a DSN for it; the probe must work with that minimal grant.
func probeRoleDSN(t *testing.T, ctx context.Context, pool execer, clusterDSN string) string {
	t.Helper()
	parsed, err := url.Parse(clusterDSN)
	if err != nil {
		t.Fatal(err)
	}
	var dbName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		t.Fatal(err)
	}
	adminURL := *parsed
	adminURL.Path = "/" + dbName
	conn, err := pgx.Connect(ctx, adminURL.String())
	if err != nil {
		t.Fatal(err)
	}
	role := fmt.Sprintf("probe_ro_%d", time.Now().UnixNano()%1_000_000_000)
	for _, stmt := range []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'probe'`, role),
		fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, quoteIdent(dbName), role),
		fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, role),
		fmt.Sprintf(`GRANT SELECT ON schema_migrations TO %s`, role),
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = conn.Exec(cleanupCtx, fmt.Sprintf(`REVOKE ALL ON schema_migrations FROM %s`, role))
		_, _ = conn.Exec(cleanupCtx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, role))
		_ = conn.Close(cleanupCtx)
	})
	roleURL := *parsed
	roleURL.Path = "/" + dbName
	roleURL.User = url.UserPassword(role, "probe")
	return roleURL.String()
}

// execer is the fixture pool surface used to create the probe role inside the
// migrated fixture database.
type execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func TestReadinessProbeBindsLiveStateAndDetectsChange(t *testing.T) {
	ctx := context.Background()
	p, _ := testdb.OpenMigrated(t)
	var generation uint64
	if err := p.QueryRow(ctx, `SELECT version FROM schema_migrations`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	root := readinessFence(t, generation)
	required := filepath.Join(t.TempDir(), "restored-manifest.json")
	if err := os.WriteFile(required, []byte(`{"fixture":"restored"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := fmt.Sprintf("URMAIL_PROBE_TEST_%d", time.Now().UnixNano())
	t.Setenv(env, probeRoleDSN(t, ctx, p, testdb.RequireDatabaseURL(t)))
	opts := readinessProbeOptions{rootDir: root, generation: generation, databaseEnv: env, requiredFiles: []string{required}}

	doc, err := probeReadiness(ctx, opts)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if doc.Format != readinessProbeFormat || doc.Generation != generation {
		t.Fatalf("unexpected probe doc: %+v", doc)
	}
	if doc.Target.Kind != "postgres" || doc.Target.SchemaVersion != generation || doc.Target.TransactionReadOnly != "on" || doc.Target.Database == "" {
		t.Fatalf("unexpected target: %+v", doc.Target)
	}
	if doc.Hold.SHA256 == "" || doc.Hold.ReceiptSHA256 == "" {
		t.Fatal("hold binding missing")
	}
	if len(doc.RequiredFiles) != 1 || doc.RequiredFiles[0].SHA256 == "" || doc.RequiredFiles[0].Size == 0 {
		t.Fatalf("required file binding missing: %+v", doc.RequiredFiles)
	}
	if doc.StateSHA256 == "" {
		t.Fatal("state hash missing")
	}

	recordPath := filepath.Join(t.TempDir(), "probe.json")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyReadinessProbe(ctx, opts, recordPath, doc); err != nil {
		t.Fatalf("verify unchanged: %v", err)
	}

	// Any bound input change is rejected before a hold is cleared.
	if err := os.WriteFile(required, []byte(`{"fixture":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := probeReadiness(ctx, opts)
	if err != nil {
		t.Fatalf("probe after change: %v", err)
	}
	if err := verifyReadinessProbe(ctx, opts, recordPath, changed); err == nil || !strings.Contains(err.Error(), "STATE_CHANGED") {
		t.Fatalf("changed state accepted: %v", err)
	}
	if err := os.WriteFile(required, []byte(`{"fixture":"restored"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Wrong expected generation is refused by the probe itself.
	if _, err := probeReadiness(ctx, readinessProbeOptions{rootDir: root, generation: generation + 1, databaseEnv: env}); err == nil || !strings.Contains(err.Error(), "does not match requested generation") {
		t.Fatalf("generation drift accepted: %v", err)
	}

	// Hold tampering breaks the receipt binding.
	holdPath := filepath.Join(root, maintenance.DefaultUpgradeHoldFile)
	holdRaw, err := os.ReadFile(holdPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(holdPath, append(holdRaw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := probeReadiness(ctx, opts); err == nil || !strings.Contains(err.Error(), "does not bind live hold") {
		t.Fatalf("tampered hold accepted: %v", err)
	}
}

func TestReadinessProbeRefusesUnprovenSources(t *testing.T) {
	ctx := context.Background()
	p, _ := testdb.OpenMigrated(t)
	root := readinessFence(t, 6)
	env := fmt.Sprintf("URMAIL_PROBE_TEST_%d", time.Now().UnixNano())
	t.Setenv(env, probeRoleDSN(t, ctx, p, testdb.RequireDatabaseURL(t)))

	// No target configured: never fall back to a default connection.
	t.Setenv(env+"_EMPTY", "")
	if _, err := probeReadiness(ctx, readinessProbeOptions{rootDir: root, generation: 6, databaseEnv: env + "_EMPTY"}); err == nil || !strings.Contains(err.Error(), "requires --sqlite or a read-only URL") {
		t.Fatalf("missing target accepted: %v", err)
	}

	// SQLite generation is checked read-only.
	sqlitePath := filepath.Join(t.TempDir(), "target.sqlite")
	db, err := sql.Open("sqlite", "file:"+sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version=6`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := probeReadiness(ctx, readinessProbeOptions{rootDir: root, generation: 6, sqlitePath: sqlitePath, sqliteVersion: 6}); err != nil {
		t.Fatalf("sqlite positive: %v", err)
	}
	if _, err := probeReadiness(ctx, readinessProbeOptions{rootDir: root, generation: 5, sqlitePath: sqlitePath, sqliteVersion: 5}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("sqlite drift accepted: %v", err)
	}
	link := sqlitePath + ".link"
	if err := os.Symlink(sqlitePath, link); err != nil {
		t.Fatal(err)
	}
	if _, err := probeReadiness(ctx, readinessProbeOptions{rootDir: root, generation: 6, sqlitePath: link, sqliteVersion: 6}); err == nil {
		t.Fatal("symlink SQLite target accepted")
	}

	// Missing required file is a hard failure, never a silent omission.
	if _, err := probeReadiness(ctx, readinessProbeOptions{rootDir: root, generation: 6, sqlitePath: sqlitePath, sqliteVersion: 6, requiredFiles: []string{filepath.Join(root, "absent.bin")}}); err == nil {
		t.Fatal("missing required file accepted")
	}
	// Without a live fence there is nothing to bind.
	emptyRoot := t.TempDir()
	if _, err := probeReadiness(ctx, readinessProbeOptions{rootDir: emptyRoot, generation: 6, sqlitePath: sqlitePath, sqliteVersion: 6}); err == nil || !strings.Contains(err.Error(), "read live upgrade fence") {
		t.Fatalf("missing fence accepted: %v", err)
	}
}

// TestReadinessProbeVerifyRejectsBadRecord keeps the verification side strict:
// an absent, symlinked or wrong-format record never authorizes a release.
func TestReadinessProbeVerifyRejectsBadRecord(t *testing.T) {
	ctx := context.Background()
	root := readinessFence(t, 6)
	sqlitePath := filepath.Join(t.TempDir(), "target.sqlite")
	db, err := sql.Open("sqlite", "file:"+sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version=6`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	opts := readinessProbeOptions{rootDir: root, generation: 6, sqlitePath: sqlitePath, sqliteVersion: 6}
	doc, err := probeReadiness(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyReadinessProbe(ctx, opts, filepath.Join(t.TempDir(), "absent.json"), doc); err == nil {
		t.Fatal("absent record accepted")
	}
	recordPath := filepath.Join(t.TempDir(), "probe.json")
	raw, _ := json.Marshal(map[string]any{"format": "other"})
	if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyReadinessProbe(ctx, opts, recordPath, doc); err == nil || !strings.Contains(err.Error(), "unsupported record format") {
		t.Fatalf("wrong-format record accepted: %v", err)
	}
}

// TestReadinessProbeRejectsBadSchemaBooks proves the probe refuses unreadable
// schema evidence instead of trusting a placeholder: dirty, empty and
// ambiguous books all fail before any readiness record could be signed.
func TestReadinessProbeRejectsBadSchemaBooks(t *testing.T) {
	cases := []struct {
		name    string
		mutate  string
		wantErr string
	}{
		{"dirty", `UPDATE schema_migrations SET dirty=true`, "is dirty"},
		{"empty", `DELETE FROM schema_migrations`, "is empty"},
		{"ambiguous", `INSERT INTO schema_migrations(version,dirty) VALUES(158,false)`, "ambiguous"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			p, _ := testdb.OpenMigrated(t)
			if _, err := p.Exec(ctx, tc.mutate); err != nil {
				t.Fatal(err)
			}
			env := fmt.Sprintf("URMAIL_PROBE_TEST_%d", time.Now().UnixNano())
			t.Setenv(env, probeRoleDSN(t, ctx, p, testdb.RequireDatabaseURL(t)))
			root := readinessFence(t, 159)
			_, err := probeReadiness(ctx, readinessProbeOptions{rootDir: root, generation: 159, databaseEnv: env})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("bad schema book accepted: %v", err)
			}
		})
	}
}

// TestReadinessProbeRejectsMixedGeneration proves a v2 hold/receipt recorded
// for another generation cannot satisfy a request for the live target.
func TestReadinessProbeRejectsMixedGeneration(t *testing.T) {
	ctx := context.Background()
	p, _ := testdb.OpenMigrated(t)
	env := fmt.Sprintf("URMAIL_PROBE_TEST_%d", time.Now().UnixNano())
	t.Setenv(env, probeRoleDSN(t, ctx, p, testdb.RequireDatabaseURL(t)))
	root := readinessFence(t, 158)
	if _, err := probeReadiness(ctx, readinessProbeOptions{rootDir: root, generation: 159, databaseEnv: env}); err == nil || !strings.Contains(err.Error(), "does not match requested generation") {
		t.Fatalf("mixed hold generation accepted: %v", err)
	}
}

// TestReadinessProbeFileAttributesAreBound proves the required file's security
// attributes, not only its bytes, participate in the binding and verify.
func TestReadinessProbeFileAttributesAreBound(t *testing.T) {
	ctx := context.Background()
	root := readinessFence(t, 6)
	sqlitePath := filepath.Join(t.TempDir(), "target.sqlite")
	db, err := sql.Open("sqlite", "file:"+sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version=6`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	required := filepath.Join(t.TempDir(), "required.json")
	if err := os.WriteFile(required, []byte(`{"mode":"bound"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := readinessProbeOptions{rootDir: root, generation: 6, sqlitePath: sqlitePath, sqliteVersion: 6, requiredFiles: []string{required}}
	doc, err := probeReadiness(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if doc.RequiredFiles[0].Mode != 0o600 {
		t.Fatalf("mode not bound: %o", doc.RequiredFiles[0].Mode)
	}
	recordPath := filepath.Join(t.TempDir(), "probe.json")
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(required, 0o640); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Chmod(required, 0o600) })
	changed, err := probeReadiness(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyReadinessProbe(ctx, opts, recordPath, changed); err == nil || !strings.Contains(err.Error(), "required file") {
		t.Fatalf("mode-only change accepted: %v", err)
	}
}

// TestReadinessProbeSQLiteIdentity proves a replacement at the same path with
// the same user_version is a different target, and a WAL target is refused
// until a WAL identity contract is frozen.
func TestReadinessProbeSQLiteIdentity(t *testing.T) {
	ctx := context.Background()
	root := readinessFence(t, 6)
	dir := t.TempDir()
	sqlitePath := filepath.Join(dir, "target.sqlite")
	makeDB := func(path, content string) {
		t.Helper()
		db, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA user_version=6`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE probe_identity(v TEXT NOT NULL); INSERT INTO probe_identity(v) VALUES(?)`, content); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
	}
	makeDB(sqlitePath, "original")
	opts := readinessProbeOptions{rootDir: root, generation: 6, sqlitePath: sqlitePath, sqliteVersion: 6}
	doc, err := probeReadiness(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Target.Inode == 0 || doc.Target.FileSHA == "" || doc.Target.Device == 0 {
		t.Fatalf("sqlite identity missing: %+v", doc.Target)
	}
	recordPath := filepath.Join(t.TempDir(), "probe.json")
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyReadinessProbe(ctx, opts, recordPath, doc); err != nil {
		t.Fatalf("sqlite verify unchanged: %v", err)
	}
	// Replace the target at the same path with the same user_version.
	replacement := filepath.Join(dir, "replacement.sqlite")
	makeDB(replacement, "replacement")
	if err := os.Rename(replacement, sqlitePath); err != nil {
		t.Fatal(err)
	}
	replaced, err := probeReadiness(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyReadinessProbe(ctx, opts, recordPath, replaced); err == nil || !strings.Contains(err.Error(), "STATE_CHANGED") {
		t.Fatalf("same-version replacement accepted: %v", err)
	}
	// A non-empty WAL means committed state outside the main file.
	if err := os.WriteFile(sqlitePath+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(sqlitePath + "-wal") })
	if _, err := probeReadiness(ctx, opts); err == nil || !strings.Contains(err.Error(), "non-empty; WAL/SHM/rollback-journal identity contract is not frozen") {
		t.Fatalf("WAL target accepted: %v", err)
	}
}

// TestReadinessProbeRejectsFullModeAndIrregularSidecars covers the QA reversal:
// a setuid required file (0600 -> 4600) and unsafe WAL/SHM sidecars are refused
// rather than treated as the same object.
func TestReadinessProbeRejectsFullModeAndIrregularSidecars(t *testing.T) {
	ctx := context.Background()
	root := readinessFence(t, 6)
	dir := t.TempDir()
	sqlitePath := filepath.Join(dir, "target.sqlite")
	db, err := sql.Open("sqlite", "file:"+sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version=6`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	required := filepath.Join(dir, "required.json")
	if err := os.WriteFile(required, []byte(`{"mode":"full"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := readinessProbeOptions{rootDir: root, generation: 6, sqlitePath: sqlitePath, sqliteVersion: 6, requiredFiles: []string{required}}
	if _, err := probeReadiness(ctx, opts); err != nil {
		t.Fatalf("positive: %v", err)
	}
	if err := syscall.Chmod(required, 0o4600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(required, 0o600) })
	if _, err := probeReadiness(ctx, opts); err == nil || !strings.Contains(err.Error(), "special permission bits") {
		t.Fatalf("setuid required file accepted: %v", err)
	}
	if err := syscall.Chmod(required, 0o600); err != nil {
		t.Fatal(err)
	}

	wal := sqlitePath + "-wal"
	if err := os.Symlink(sqlitePath, wal); err != nil {
		t.Fatal(err)
	}
	if _, err := probeReadiness(ctx, opts); err == nil || !strings.Contains(err.Error(), "unsafe link or irregular") {
		t.Fatalf("symlink WAL accepted: %v", err)
	}
	_ = os.Remove(wal)
	shm := sqlitePath + "-shm"
	if err := os.Mkdir(shm, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := probeReadiness(ctx, opts); err == nil || !strings.Contains(err.Error(), "unsafe link or irregular") {
		t.Fatalf("directory SHM accepted: %v", err)
	}
	_ = os.Remove(shm)
	// A zero-length regular sidecar is inert and stays allowed.
	if err := os.WriteFile(wal, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := probeReadiness(ctx, opts); err != nil {
		t.Fatalf("zero-length WAL rejected: %v", err)
	}
	if err := os.WriteFile(wal, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(wal) })
	if _, err := probeReadiness(ctx, opts); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("non-empty WAL accepted: %v", err)
	}
}

// TestReadinessProbeRefusesWALModeWithoutSidecars proves the WAL journal mode
// is detected from the file header before any driver opens it: a WAL database
// with no sidecars must be refused without creating -shm/-wal or touching the
// target, while a rollback-journal snapshot stays readable with zero writes.
func TestReadinessProbeRefusesWALModeWithoutSidecars(t *testing.T) {
	ctx := context.Background()
	root := readinessFence(t, 6)
	dir := t.TempDir()
	walPath := filepath.Join(dir, "wal.sqlite")
	db, err := sql.Open("sqlite", "file:"+walPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version=6`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE probe_identity(v TEXT NOT NULL); INSERT INTO probe_identity(v) VALUES('wal')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(walPath + suffix) // build the no-sidecar WAL case
	}
	snapshot := func() map[string]int64 {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				t.Fatal(err)
			}
			out[entry.Name()] = info.Size()
		}
		return out
	}
	before := snapshot()
	opts := readinessProbeOptions{rootDir: root, generation: 6, sqlitePath: walPath, sqliteVersion: 6}
	if _, err := probeReadiness(ctx, opts); err == nil || !strings.Contains(err.Error(), "WAL mode") {
		t.Fatalf("WAL-mode target accepted: %v", err)
	}
	after := snapshot()
	if len(before) != len(after) {
		t.Fatalf("WAL probe changed the directory: before=%v after=%v", before, after)
	}
	for name, size := range before {
		if after[name] != size {
			t.Fatalf("WAL probe changed %s: %d -> %d", name, size, after[name])
		}
	}

	// Rollback-journal snapshot: probe twice with zero writes, including verify.
	rollback := filepath.Join(dir, "rollback.sqlite")
	r, err := sql.Open("sqlite", "file:"+rollback)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Exec(`PRAGMA user_version=6`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Exec(`CREATE TABLE probe_identity(v TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	before = snapshot()
	opts.sqlitePath = rollback
	doc, err := probeReadiness(ctx, opts)
	if err != nil {
		t.Fatalf("rollback journal snapshot refused: %v", err)
	}
	after = snapshot()
	if len(before) != len(after) {
		t.Fatalf("rollback probe changed the directory: before=%v after=%v", before, after)
	}
	recordPath := filepath.Join(t.TempDir(), "probe.json")
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := probeReadiness(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyReadinessProbe(ctx, opts, recordPath, second); err != nil {
		t.Fatalf("rollback verify: %v", err)
	}
	third := snapshot()
	if len(before) != len(third) {
		t.Fatalf("verify changed the directory: before=%v after=%v", before, third)
	}
	for name, size := range before {
		if third[name] != size {
			t.Fatalf("verify changed %s: %d -> %d", name, size, third[name])
		}
	}
}

// TestInotifyWatchDetectsConcurrentWrite exercises the snapshot write detector
// directly: a write after the watch starts must be reported, and a quiet file
// must not.
func TestInotifyWatchDetectsConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	quiet := filepath.Join(dir, "quiet")
	if err := os.WriteFile(quiet, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	watch, err := watchForWrites(quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := watch.NoWrites(); err != nil {
		t.Fatalf("quiet file reported a write: %v", err)
	}
	watch.Close()
	busy := filepath.Join(dir, "busy")
	if err := os.WriteFile(busy, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	watch, err = watchForWrites(busy)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	if err := os.WriteFile(busy, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := watch.NoWrites(); err == nil || !strings.Contains(err.Error(), "received a write") {
		t.Fatalf("concurrent write not detected: %v", err)
	}
}

// TestReadinessProbeRejectsRollbackJournalSidecar proves a non-empty rollback
// journal is refused like WAL/SHM before the driver can act on it.
func TestReadinessProbeRejectsRollbackJournalSidecar(t *testing.T) {
	ctx := context.Background()
	root := readinessFence(t, 6)
	dir := t.TempDir()
	path := filepath.Join(dir, "target.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version=6`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	opts := readinessProbeOptions{rootDir: root, generation: 6, sqlitePath: path, sqliteVersion: 6}
	if _, err := probeReadiness(ctx, opts); err != nil {
		t.Fatalf("positive: %v", err)
	}
	journal := path + "-journal"
	if err := os.WriteFile(journal, []byte("hot"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(journal) })
	if _, err := probeReadiness(ctx, opts); err == nil || !strings.Contains(err.Error(), "rollback-journal") {
		t.Fatalf("non-empty rollback journal accepted: %v", err)
	}
}
