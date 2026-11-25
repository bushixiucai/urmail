package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
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

// TestFirstBootPrepareRunsAsContainerIdentity proves the container topology:
// first boot renders as the unprivileged owner of the config volume, while the
// fenced upgrade entry stays root-only.
func TestFirstBootPrepareRunsAsContainerIdentity(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this process is root; the unprivileged path is exercised elsewhere")
	}
	isolateOfflineRoots(t)
	status, err := PrepareOfflineFirstBoot(context.Background(), firstBootConfig(), emptySQLitePool(t), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("non-root first boot refused: %v", err)
	}
	if status.Step != "prepared" {
		t.Fatalf("first boot status %+v", status)
	}
	if _, err = PrepareOffline(context.Background(), firstBootConfig(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("fenced upgrade preparation no longer requires root")
	}
}

func TestFirstBootPrepareUsesConfiguredStateRootAndRenderedDir(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	prevTarget, prevCmd := offlineTargetRoot, offlineCommand
	offlineTargetRoot = filepath.Join(tree, "system")
	manager, err := NewTestApplyManager(tree, firstBootConfig())
	if err != nil {
		t.Fatal(err)
	}
	offlineCommand = manager.run
	t.Cleanup(func() {
		offlineTargetRoot, offlineCommand = prevTarget, prevCmd
	})

	stateDir := filepath.Join(t.TempDir(), "agent-state")
	renderDir := filepath.Join(t.TempDir(), "config")
	cfg := firstBootConfig()
	cfg.AgentStateRoot = stateDir
	cfg.RenderedConfigDir = renderDir

	status, err := PrepareOfflineFirstBoot(context.Background(), cfg, emptySQLitePool(t), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("PrepareOfflineFirstBoot with AgentStateRoot+RenderedConfigDir: %v", err)
	}
	if status.Step != "prepared" {
		t.Fatalf("first boot status %+v", status)
	}
	if _, err = os.Stat(filepath.Join(stateDir, "manifest.json")); err != nil {
		t.Fatalf("manifest missing in configured AgentStateRoot %s: %v", stateDir, err)
	}
}

func TestFirstBootPrepareFailsClosedOnUnwritableStateRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	lockedParent := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(lockedParent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedParent, 0o755) })

	cfg := firstBootConfig()
	cfg.AgentStateRoot = filepath.Join(lockedParent, "state")
	if _, err := PrepareOfflineFirstBoot(context.Background(), cfg, emptySQLitePool(t), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected fail-closed error for unwritable AgentStateRoot")
	}
}

func TestApplyWriteSkipsChownGroupWhenNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this process is root; non-root chown skip is exercised as unprivileged user")
	}
	tree := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := firstBootConfig()
	cfg.RenderedConfigDir = "/srv/urmail/data/config"
	m, err := NewTestApplyManager(tree, cfg)
	if err != nil {
		t.Fatal(err)
	}
	dbConf := "/srv/urmail/data/config/dovecot/conf.d/urmail/db.conf"
	if err := m.write(dbConf, []byte("driver = sqlite\n"), 0o640); err != nil {
		t.Fatalf("non-root write(db.conf) failed: %v", err)
	}
	if _, err := os.Stat(m.target(dbConf)); err != nil {
		t.Fatalf("db.conf missing after non-root write: %v", err)
	}
	privKey := "/etc/opendkim/keys/example.com/mail.private"
	if err := m.write(privKey, []byte("PRIVATE"), 0o640); err != nil {
		t.Fatalf("non-root write(.private) failed: %v", err)
	}
	if _, err := os.Stat(m.target(privKey)); err != nil {
		t.Fatalf("mail.private missing after non-root write: %v", err)
	}
}

func TestCheckComposeModeAndHostModeMatrix(t *testing.T) {
	t.Run("compose_skips_host_checks_and_targets_rendered_root", func(t *testing.T) {
		var ran []string
		cfg := firstBootConfig()
		cfg.RenderedConfigDir = "/srv/urmail/data/config"
		m := &ApplyManager{
			cfg: cfg,
			run: func(_ context.Context, name string, args ...string) error {
				ran = append(ran, name+" "+strings.Join(args, " "))
				return nil
			},
		}
		params := ApplyParams{}
		params.DNS.Zones = []nsd.Zone{{Domain: "example.com"}}
		if err := m.check(context.Background(), params); err != nil {
			t.Fatalf("compose check failed: %v", err)
		}
		want := []string{
			"/usr/bin/doveconf -n -c /srv/urmail/data/config/dovecot/dovecot.conf",
			"/usr/sbin/postfix -c /srv/urmail/data/config/postfix check",
		}
		if strings.Join(ran, "\n") != strings.Join(want, "\n") {
			t.Fatalf("compose check commands:\ngot:\n%s\nwant:\n%s", strings.Join(ran, "\n"), strings.Join(want, "\n"))
		}
	})

	t.Run("compose_fails_closed_when_rendered_check_fails", func(t *testing.T) {
		cfg := firstBootConfig()
		cfg.RenderedConfigDir = "/srv/urmail/data/config"
		m := &ApplyManager{
			cfg: cfg,
			run: func(_ context.Context, name string, args ...string) error {
				if filepath.Base(name) == "doveconf" {
					return errors.New("fatal: open(/srv/urmail/data/config/dovecot/dovecot.conf) failed: No such file or directory")
				}
				return nil
			},
		}
		err := m.check(context.Background(), ApplyParams{})
		if err == nil || !strings.Contains(err.Error(), "check doveconf") {
			t.Fatalf("expected fail-closed doveconf error, got %v", err)
		}
	})

	t.Run("host_mode_runs_all_host_checks", func(t *testing.T) {
		var ran []string
		m := &ApplyManager{
			cfg: firstBootConfig(),
			run: func(_ context.Context, name string, args ...string) error {
				ran = append(ran, name+" "+strings.Join(args, " "))
				return nil
			},
		}
		params := ApplyParams{}
		params.DNS.Zones = []nsd.Zone{{Domain: "example.com"}}
		if err := m.check(context.Background(), params); err != nil {
			t.Fatalf("host check failed: %v", err)
		}
		for _, expected := range []string{"opendkim", "doveconf -n", "postfix check", "nginx -t", "nsd-checkconf", "nsd-checkzone"} {
			if !strings.Contains(strings.Join(ran, "\n"), expected) {
				t.Fatalf("host check missing %q in:\n%s", expected, strings.Join(ran, "\n"))
			}
		}
	})
}
