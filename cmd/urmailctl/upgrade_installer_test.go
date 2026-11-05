package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

func TestInstallerLeaseChildHelper(t *testing.T) {
	if os.Getenv("URMAIL_LEASE_TEST_CHILD") != "1" {
		return
	}
	if err := maintenance.CheckInstallerLease(currentMaintenanceRoot()); err != nil {
		t.Fatal(err)
	}
	lock, err := maintenance.AcquireLock(currentMaintenanceRoot(), maintenance.DefaultLockName)
	if err != nil {
		t.Fatal(err)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err = maintenance.CheckInstallerLease(currentMaintenanceRoot()); err != nil {
		t.Fatal(err)
	}
}

func TestInstallerRunnerRealRootLease(t *testing.T) {
	if os.Geteuid() != 0 || runtime.GOOS != "linux" {
		t.Skip("requires Linux root with bash")
	}
	dir := t.TempDir()
	t.Setenv("URMAIL_MAINTENANCE_ROOT", filepath.Join(dir, "maintenance"))
	t.Setenv("URMAIL_LEASE_TEST_CHILD", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A private /tmp staging directory is trusted, unlike the writable /tmp root.
	stage, err := os.MkdirTemp("/tmp", "urmail-lease-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	script := filepath.Join(stage, "installer.sh")
	ready, finish := filepath.Join(dir, "ready"), filepath.Join(dir, "finish")
	text := fmt.Sprintf("set -eu\n%q -test.run '^TestInstallerLeaseChildHelper$'\ntouch %q\nwhile [ ! -f %q ]; do sleep 0.02; done\nexit 2\n", binary, ready, finish)
	if err = os.WriteFile(script, []byte(text), 0o700); err != nil {
		t.Fatal(err)
	}
	// Injected shell startup files must not execute.
	poison := filepath.Join(dir, "poison")
	t.Setenv("BASH_ENV", poison)
	if err = os.WriteFile(poison, []byte("exit 99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	go func() { done <- runUpgradeInstaller(ctx, []string{"--installer", script}) }()
	defer func() { _ = os.WriteFile(finish, nil, 0o600) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not validate inherited lease")
		}
		time.Sleep(10 * time.Millisecond)
	}
	root := currentMaintenanceRoot()
	if err = maintenance.CreateUpgradeFence(root, maintenance.UpgradeFenceMeta{Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err = runUpgradeFence(t.Context(), []string{"--timeout", "0s", "--root-dir", root}); !errors.Is(err, maintenance.ErrLockHeld) {
		t.Fatalf("concurrent fence: %v", err)
	}
	if err = runUpgradeRelease(t.Context(), []string{"--timeout", "0s", "--root-dir", root}); !errors.Is(err, maintenance.ErrLockHeld) {
		t.Fatalf("concurrent release: %v", err)
	}
	short, cancelShort := context.WithTimeout(t.Context(), 100*time.Millisecond)
	err = runConfigPrepare(short, nil)
	cancelShort()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent prepare: %v", err)
	}
	if err = os.WriteFile(finish, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err = <-done
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("child nonzero lost: %v", err)
	}
	if !maintenance.IsUpgradeHeld(root) {
		t.Fatal("runner released persistent fence")
	}
	lock, err := maintenance.AcquireLock(root, maintenance.DefaultLockName)
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
}

func TestRollbackRequiresCheckpointIntegrityVerification(t *testing.T) {
	allowUpgradeTestUser(t)
	root := t.TempDir()
	if err := maintenance.CreateUpgradeFence(root, maintenance.UpgradeFenceMeta{Version: 1}); err != nil {
		t.Fatal(err)
	}
	checkpoint := t.TempDir()
	if err := os.WriteFile(filepath.Join(checkpoint, "database.dump"), []byte("not a verified checkpoint"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := activeCheckpointVerifier
	t.Cleanup(func() { activeCheckpointVerifier = previous })
	denied := errors.New("manifest digest mismatch")
	called := false
	activeCheckpointVerifier = func(_ context.Context, path string) error {
		called = true
		if path != checkpoint {
			t.Fatalf("wrong checkpoint: %s", path)
		}
		return denied
	}
	err := runUpgradeRollback(t.Context(), []string{"--root-dir", root, "--checkpoint", checkpoint})
	if !called || !errors.Is(err, denied) {
		t.Fatalf("rollback accepted unverified dump: %v", err)
	}
	if !maintenance.IsUpgradeHeld(root) {
		t.Fatal("rollback cleared fence")
	}
	activeCheckpointVerifier = func(context.Context, string) error { return nil }
	if err = runUpgradeRollback(t.Context(), []string{"--root-dir", root, "--checkpoint", checkpoint}); err != nil {
		t.Fatal(err)
	}
	if !maintenance.IsUpgradeHeld(root) {
		t.Fatal("verified rollback cleared fence")
	}
}

func allowUpgradeTestUser(t *testing.T) {
	t.Helper()
	// CI runners expose a real systemd but deny unprivileged daemon-reload;
	// stub it so these tests exercise the same code path as a macOS dev box
	// (where systemctl is absent and SystemctlReload is a no-op).
	prevReload := maintenance.SystemctlReloadFunc
	maintenance.SystemctlReloadFunc = func(context.Context) error { return nil }
	t.Cleanup(func() { maintenance.SystemctlReloadFunc = prevReload })
	old := upgradeEffectiveUID
	upgradeEffectiveUID = func() int { return 0 }
	t.Cleanup(func() { upgradeEffectiveUID = old })
}

func TestUpgradePrivilegesIgnoreEnvironmentBypass(t *testing.T) {
	old := upgradeEffectiveUID
	upgradeEffectiveUID = func() int { return 1234 }
	t.Cleanup(func() { upgradeEffectiveUID = old })
	t.Setenv("URMAIL_ALLOW_TEST_USER", "1")
	if err := checkUpgradePrivileges(); err == nil {
		t.Fatal("environment bypass authorized non-root")
	}
}

func TestTrustedInstallerRejectsUnsafePaths(t *testing.T) {
	for _, path := range []string{"", "relative.sh", "/tmp/../tmp/script.sh"} {
		if file, err := openTrustedInstaller(path); err == nil {
			file.Close()
			t.Fatalf("accepted %q", path)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "installer.sh")
	if err := os.WriteFile(path, []byte("exit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if file, err := openTrustedInstaller(path); err == nil {
			file.Close()
			t.Fatal("accepted non-root-owned script")
		}
		if err := runUpgradeInstaller(t.Context(), []string{"--installer", path}); err == nil {
			t.Fatal("non-root execution allowed")
		}
		return
	}
	// Linux temp ancestors are root-owned sticky /tmp; macOS /var is a symlink,
	// intentionally rejected by this strict production Linux path validator.
	if file, err := openTrustedInstaller(path); err == nil {
		file.Close()
	} else if runtime.GOOS == "linux" {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o722); err != nil {
		t.Fatal(err)
	}
	if file, err := openTrustedInstaller(path); err == nil {
		file.Close()
		t.Fatal("accepted writable script")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.sh")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if file, err := openTrustedInstaller(link); err == nil {
		file.Close()
		t.Fatal("accepted symlink")
	}
}
