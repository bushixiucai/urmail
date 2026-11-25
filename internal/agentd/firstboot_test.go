package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

// isolateOfflineRoots points both preparation entries at one private tree
// and the standard test command runner, which simulates the host tools.
func isolateOfflineRoots(t *testing.T) {
	t.Helper()
	tree := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	previousState, previousTarget, previousCommand := offlineStateRoot, offlineTargetRoot, offlineCommand
	offlineStateRoot = filepath.Join(tree, "state")
	offlineTargetRoot = filepath.Join(tree, "system")
	t.Cleanup(func() {
		offlineStateRoot, offlineTargetRoot, offlineCommand = previousState, previousTarget, previousCommand
	})
	manager, err := NewTestApplyManager(tree, firstBootConfig())
	if err != nil {
		t.Fatal(err)
	}
	offlineCommand = manager.run
}

func firstBootConfig() *config.Config {
	return &config.Config{
		MyHostname:         "box.example.com",
		PrimaryDomain:      "example.com",
		StorageRoot:        "/srv/urmail/data/mail",
		DatabaseURLDovecot: "host=127.0.0.1 user=dovecot password=secret",
		ListenAddr:         "127.0.0.1:8080",
		PublicIPv4:         "192.0.2.1",
		Nameservers:        []string{"ns.example.com"},
	}
}

func emptySQLitePool(t *testing.T) *db.Pool {
	t.Helper()
	path := filepath.Join(t.TempDir(), "urmail.sqlite")
	raw, err := sqlitedb.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlitedb.Migrate(context.Background(), raw, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	pool, err := db.NewSQLitePool(raw, isolation.ScopePlatform)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_ = raw.Close()
	})
	return pool
}

func TestFirstBootPrepareRendersEmptySQLiteRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("first-boot preparation writes root-owned agent state")
	}
	isolateOfflineRoots(t)
	status, err := PrepareOfflineFirstBoot(context.Background(), firstBootConfig(), emptySQLitePool(t), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if status.Ready || status.Step != "prepared" || status.Desired == "" {
		t.Fatalf("first boot status %+v", status)
	}
	if _, err = os.Stat(filepath.Join(offlineStateRoot, "manifest.json")); err != nil {
		t.Fatalf("rendered generation missing: %v", err)
	}
}

func TestFirstBootPrepareRefusesExistingGeneration(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("first-boot preparation writes root-owned agent state")
	}
	isolateOfflineRoots(t)
	pool := emptySQLitePool(t)
	if _, err := PrepareOfflineFirstBoot(context.Background(), firstBootConfig(), pool, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	// A second invocation sees the generation the first one recorded.
	_, err := PrepareOfflineFirstBoot(context.Background(), firstBootConfig(), pool, json.RawMessage(`{}`))
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("repeat first boot err=%v, want ErrRejected", err)
	}
}

func TestFirstBootPrepareRejectsNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this process is root")
	}
	_, err := PrepareOfflineFirstBoot(context.Background(), firstBootConfig(), nil, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("non-root first boot was accepted")
	}
}
