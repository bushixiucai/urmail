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
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/maintenance"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func readinessFence(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	meta := maintenance.UpgradeFenceMeta{Version: 2, CreatedAt: time.Now().UTC(), Reason: "readiness probe fixture", Operator: "fixture", Units: []string{"postfix.service"}}
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
	root := readinessFence(t)
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
	raw, err := canonicalJSON(doc)
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
	if _, err := probeReadiness(ctx, readinessProbeOptions{rootDir: root, generation: generation + 1, databaseEnv: env}); err == nil || !strings.Contains(err.Error(), "does not match expected generation") {
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
	root := readinessFence(t)
	env := fmt.Sprintf("URMAIL_PROBE_TEST_%d", time.Now().UnixNano())
	t.Setenv(env, probeRoleDSN(t, ctx, p, testdb.RequireDatabaseURL(t)))

	// No target configured: never fall back to a default connection.
	t.Setenv(env+"_EMPTY", "")
	if _, err := probeReadiness(ctx, readinessProbeOptions{rootDir: root, generation: 159, databaseEnv: env + "_EMPTY"}); err == nil || !strings.Contains(err.Error(), "requires --sqlite or a read-only URL") {
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
	if _, err := probeReadiness(ctx, readinessProbeOptions{rootDir: root, generation: 5, sqlitePath: sqlitePath, sqliteVersion: 5}); err == nil || !strings.Contains(err.Error(), "does not match expected generation") {
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
	root := readinessFence(t)
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
			root := readinessFence(t)
			_, err := probeReadiness(ctx, readinessProbeOptions{rootDir: root, generation: 159, databaseEnv: env})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("bad schema book accepted: %v", err)
			}
		})
	}
}
