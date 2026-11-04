package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

func TestUpgradeFenceLifecycleAndConcurrencyRejection(t *testing.T) {
	allowUpgradeTestUser(t)

	maintDir := t.TempDir()
	dropInDir := t.TempDir()
	t.Setenv("URMAIL_MAINTENANCE_ROOT", maintDir)

	// Mock systemd writer units behavior in test environment
	mockUnits := map[string]bool{
		"dovecot.socket":           true,
		"postfix@-.service":        true,
		"postfix.service":          true,
		"dovecot.service":          true,
		"urmail-usage-sync.timer":  true,
		"urmail-cert-renew.timer":  true,
		"urmail-backup-auto.timer": true,
	}
	unitHistory := []string{}
	reset := setBackupUnitActionForTest(func(_ context.Context, action, unit string) (bool, error) {
		unitHistory = append(unitHistory, action+":"+unit)
		switch action {
		case "state":
			return mockUnits[unit], nil
		case "stop":
			mockUnits[unit] = false
			return false, nil
		case "start":
			mockUnits[unit] = true
			return true, nil
		default:
			return false, errors.New("unknown unit action")
		}
	})
	defer reset()

	// Setup isolated test env file
	envFile := filepath.Join(maintDir, "urmail.env")
	_ = os.WriteFile(envFile, []byte("URMAIL_DATABASE_URL=postgres://app@localhost/urmail\nURMAIL_DATABASE_URL_PLATFORM=postgres://platform@localhost/urmail\nURMAIL_QUOTE_HMAC_KEY=test-key\nURMAIL_POLICY_ENABLED=true\n"), 0o600)
	t.Setenv("URMAIL_ENV_FILE", envFile)
	ctx := t.Context()

	resetVerifier := setMigrationVerifierForTest(func(_ context.Context, _ string) error {
		return nil
	})
	defer resetVerifier()

	// 1. Initially status is inactive
	status, err := maintenance.CheckFenceStatus(maintDir)
	if err != nil {
		t.Fatalf("CheckFenceStatus failed: %v", err)
	}
	if status.Active {
		t.Fatal("expected fence not active initially")
	}

	// 2. Establish fence via urmailctl upgrade fence
	fenceArgs := []string{
		"fence",
		"--reason", "Test automated upgrade",
		"--timeout", "5s",
		"--root-dir", maintDir,
		"--drop-in-dir", dropInDir,
	}
	if err = runUpgrade(ctx, fenceArgs); err != nil {
		t.Fatalf("runUpgrade fence failed: %v", err)
	}

	// Verify fence active
	if !maintenance.IsUpgradeHeld(maintDir) {
		t.Fatal("expected IsUpgradeHeld to be true")
	}
	if !backupWritersHeld() {
		t.Fatal("expected backupWritersHeld to be true while upgrade fence is active")
	}

	// Verify drop-ins were installed
	guards, err := maintenance.CheckDropInGuards(dropInDir, maintenance.DropInGuardedUnits)
	if err != nil {
		t.Fatalf("CheckDropInGuards failed: %v", err)
	}
	for _, unit := range maintenance.DropInGuardedUnits {
		if !guards[unit] {
			t.Fatalf("expected drop-in guard installed for %s", unit)
		}
	}

	// Verify active units were recorded and stopped
	status, err = maintenance.CheckFenceStatus(maintDir)
	if err != nil {
		t.Fatalf("CheckFenceStatus failed: %v", err)
	}
	if status.Receipt == nil || len(status.Receipt.Units) != 7 {
		t.Fatalf("expected 7 recorded active units in receipt, got: %v", status.Receipt)
	}

	// Activation sockets must close before stopping the service/real Postfix
	// instance; templates receive guards but must never be state/stop targets.
	stopOrder := map[string]int{}
	for index, action := range unitHistory {
		if strings.Contains(action, "postfix@.service") {
			t.Fatalf("template used as concrete target: %s", action)
		}
		if strings.HasPrefix(action, "stop:") {
			stopOrder[strings.TrimPrefix(action, "stop:")] = index
		}
	}
	for _, service := range []string{"postfix.service", "postfix@-.service", "dovecot.service"} {
		if stopOrder["dovecot.socket"] >= stopOrder[service] {
			t.Fatalf("socket must stop before %s: %v", service, unitHistory)
		}
	}
	for _, unit := range []string{"dovecot.socket", "postfix@-.service"} {
		found := false
		for _, recorded := range status.Receipt.Units {
			if recorded == unit {
				found = true
			}
		}
		if !found || mockUnits[unit] {
			t.Fatalf("concrete activation/writer not recorded and stopped: %s", unit)
		}
	}

	// 3. Concurrency rejection: verify backup, backup-auto, restore, restore-mailbox refuse while fence is held
	backupErr := runBackup(ctx, []string{"--out-dir", t.TempDir()})
	if backupErr == nil || !strings.Contains(backupErr.Error(), "upgrade hold") {
		t.Fatalf("expected backup refused error, got: %v", backupErr)
	}

	restoreErr := runRestore(ctx, []string{"--dump", filepath.Join(t.TempDir(), "dummy.dump"), "--force"})
	if restoreErr == nil || !strings.Contains(restoreErr.Error(), "upgrade hold") {
		t.Fatalf("expected restore refused error, got: %v", restoreErr)
	}

	restoreMailboxErr := runRestore(ctx, []string{
		"mailbox",
		"--tenant-id", "1",
		"--mailbox-id", "2",
		"--address", "user@example.test",
		"--manifest", filepath.Join(t.TempDir(), "manifest.json"),
		"--mail", filepath.Join(t.TempDir(), "mail.tar.gz"),
	})
	if restoreMailboxErr == nil || !strings.Contains(restoreMailboxErr.Error(), "upgrade hold") {
		t.Fatalf("expected restore mailbox refused error, got: %v", restoreMailboxErr)
	}

	usageSyncErr := runUsageSync(ctx, []string{"sync"})
	if usageSyncErr == nil || (!strings.Contains(usageSyncErr.Error(), "upgrade hold") && (os.Geteuid() == 0 || !strings.Contains(usageSyncErr.Error(), "root"))) {
		t.Fatalf("expected usage sync refused error, got: %v", usageSyncErr)
	}

	certErr := runCert(ctx, []string{"issue", "example.com"})
	if certErr == nil || !strings.Contains(certErr.Error(), "upgrade hold") {
		t.Fatalf("expected cert operations refused error, got: %v", certErr)
	}

	// 4. Test status command
	statusArgs := []string{
		"status",
		"--root-dir", maintDir,
		"--drop-in-dir", dropInDir,
	}
	if err = runUpgrade(ctx, statusArgs); err != nil {
		t.Fatalf("runUpgrade status failed: %v", err)
	}

	// 5. Test idempotency of fence establishment
	if err = runUpgrade(ctx, fenceArgs); err != nil {
		t.Fatalf("subsequent runUpgrade fence failed: %v", err)
	}

	// 6. Release fence verification: default release MUST be rejected without break-glass
	defaultReleaseArgs := []string{
		"release",
		"--root-dir", maintDir,
		"--drop-in-dir", dropInDir,
	}
	defaultErr := runUpgrade(ctx, defaultReleaseArgs)
	if defaultErr == nil || !strings.Contains(defaultErr.Error(), "--break-glass --ack-risk") {
		t.Fatalf("expected release without break-glass to be rejected, got: %v", defaultErr)
	}

	// Partial break-glass without ack-risk must also fail
	partialReleaseArgs := []string{
		"release",
		"--root-dir", maintDir,
		"--drop-in-dir", dropInDir,
		"--break-glass",
	}
	partialErr := runUpgrade(ctx, partialReleaseArgs)
	if partialErr == nil || !strings.Contains(partialErr.Error(), "--break-glass --ack-risk") {
		t.Fatalf("expected partial release without ack-risk to be rejected, got: %v", partialErr)
	}

	// Explicit break-glass and ack-risk succeeds
	releaseArgs := []string{
		"release",
		"--root-dir", maintDir,
		"--drop-in-dir", dropInDir,
		"--break-glass",
		"--ack-risk",
	}
	if err = runUpgrade(ctx, releaseArgs); err != nil {
		t.Fatalf("runUpgrade release with break-glass failed: %v", err)
	}

	// Verify fence cleared
	if maintenance.IsUpgradeHeld(maintDir) {
		t.Fatal("expected IsUpgradeHeld to be false after release")
	}
	if backupWritersHeld() {
		t.Fatal("expected backupWritersHeld to be false after release")
	}

	// Verify drop-ins removed
	guards, err = maintenance.CheckDropInGuards(dropInDir, maintenance.DropInGuardedUnits)
	if err != nil {
		t.Fatalf("CheckDropInGuards failed: %v", err)
	}
	for _, unit := range maintenance.DropInGuardedUnits {
		if guards[unit] {
			t.Fatalf("expected drop-in guard removed for %s", unit)
		}
	}

	// Verify originally active units were restarted
	for _, u := range []string{"postfix.service", "postfix@-.service", "dovecot.socket", "dovecot.service", "urmail-usage-sync.timer"} {
		if !mockUnits[u] {
			t.Fatalf("expected unit %s to be restarted on release", u)
		}
	}

	// 7. Idempotent duplicate release
	if err = runUpgrade(ctx, releaseArgs); err != nil {
		t.Fatalf("subsequent runUpgrade release failed: %v", err)
	}
}

func TestUpgradeBehaviorFaultInjectionStopFailureRetainsFence(t *testing.T) {
	allowUpgradeTestUser(t)

	maintDir := t.TempDir()
	dropInDir := t.TempDir()
	t.Setenv("URMAIL_MAINTENANCE_ROOT", maintDir)
	ctx := t.Context()

	// Fault injection: mock systemctl stop returning an unexpected failure
	reset := setBackupUnitActionForTest(func(_ context.Context, action, unit string) (bool, error) {
		if action == "state" {
			return true, nil
		}
		if action == "stop" && unit == "dovecot.service" {
			return false, errors.New("simulated systemd stop timeout / deadlock")
		}
		return false, nil
	})
	defer reset()

	fenceArgs := []string{
		"fence",
		"--reason", "Test fault injection",
		"--root-dir", maintDir,
		"--drop-in-dir", dropInDir,
	}
	err := runUpgrade(ctx, fenceArgs)
	if err == nil {
		t.Fatal("expected runUpgrade fence to fail when unit stop fails")
	}
	if !strings.Contains(err.Error(), "failed to stop writer units (fence retained)") {
		t.Fatalf("expected fence retained error, got: %v", err)
	}

	// CRITICAL ARCHITECTURAL GUARANTEE:
	// If writer unit fails to stop, upgrade fence and drop-in guards MUST NOT be removed!
	if !maintenance.IsUpgradeHeld(maintDir) {
		t.Fatal("CRITICAL: upgrade.hold MUST be retained after stop failure")
	}
	guards, _ := maintenance.CheckDropInGuards(dropInDir, maintenance.DropInGuardedUnits)
	for _, unit := range maintenance.DropInGuardedUnits {
		if !guards[unit] {
			t.Fatalf("CRITICAL: drop-in guard for %s MUST be retained after stop failure", unit)
		}
	}
}

func TestUpgradeBehaviorFaultInjectionStateInspectionFailureFailsClosed(t *testing.T) {
	allowUpgradeTestUser(t)

	maintDir := t.TempDir()
	dropInDir := t.TempDir()
	t.Setenv("URMAIL_MAINTENANCE_ROOT", maintDir)
	ctx := t.Context()

	// Fault injection: mock systemctl show returning DBus / systemd failure
	reset := setBackupUnitActionForTest(func(_ context.Context, action, _ string) (bool, error) {
		if action == "state" {
			return false, errors.New("systemd dbus communication broken")
		}
		return false, nil
	})
	defer reset()

	fenceArgs := []string{
		"fence",
		"--reason", "Test state inspection failure",
		"--root-dir", maintDir,
		"--drop-in-dir", dropInDir,
	}
	err := runUpgrade(ctx, fenceArgs)
	if err == nil {
		t.Fatal("expected runUpgrade fence to fail closed on inspection failure")
	}
	if !strings.Contains(err.Error(), "inspect writer unit") {
		t.Fatalf("expected inspect writer unit error, got: %v", err)
	}
}

func TestUpgradeInterruptedSiteRecovery(t *testing.T) {
	allowUpgradeTestUser(t)

	maintDir := t.TempDir()
	dropInDir := t.TempDir()
	t.Setenv("URMAIL_MAINTENANCE_ROOT", maintDir)
	ctx := t.Context()

	reset := setBackupUnitActionForTest(func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	})
	defer reset()

	// Simulate an interrupted upgrade: fence was established but process terminated mid-stream
	meta := maintenance.UpgradeFenceMeta{
		Version:   1,
		CreatedAt: time.Now().UTC(),
		Reason:    "interrupted midway test",
		Operator:  "test-operator",
		Units:     []string{"postfix.service", "dovecot.service"},
	}
	if err := maintenance.CreateUpgradeFence(maintDir, meta); err != nil {
		t.Fatalf("failed to create simulated interrupted fence: %v", err)
	}
	holdFile := filepath.Join(maintDir, maintenance.DefaultUpgradeHoldFile)
	if _, err := maintenance.InstallDropInGuards(dropInDir, holdFile, maintenance.DropInGuardedUnits); err != nil {
		t.Fatalf("failed to install drop-in guards: %v", err)
	}

	// Check that site remains fenced and writers held
	if !maintenance.IsUpgradeHeld(maintDir) || !backupWritersHeld() {
		t.Fatal("interrupted site must remain fenced and held")
	}

	// Rollback guidance reports checkpoint requirements and retains fence
	rollbackArgs := []string{
		"rollback",
		"--root-dir", maintDir,
	}
	if err := runUpgrade(ctx, rollbackArgs); err != nil {
		t.Fatalf("runUpgrade rollback failed: %v", err)
	}
	if !maintenance.IsUpgradeHeld(maintDir) {
		t.Fatal("rollback must preserve the upgrade fence")
	}

	// Operator executes release to clear the interrupted site
	// Setup isolated test env file
	envFile := filepath.Join(maintDir, "urmail.env")
	_ = os.WriteFile(envFile, []byte("URMAIL_DATABASE_URL=postgres://app@localhost/urmail\nURMAIL_DATABASE_URL_PLATFORM=postgres://platform@localhost/urmail\nURMAIL_QUOTE_HMAC_KEY=test-key\nURMAIL_POLICY_ENABLED=true\n"), 0o600)
	t.Setenv("URMAIL_ENV_FILE", envFile)

	resetVerifier := setMigrationVerifierForTest(func(_ context.Context, _ string) error {
		return nil
	})
	defer resetVerifier()

	releaseArgs := []string{
		"release",
		"--root-dir", maintDir,
		"--drop-in-dir", dropInDir,
		"--skip-restart",
		"--break-glass",
		"--ack-risk",
	}
	if err := runUpgrade(ctx, releaseArgs); err != nil {
		t.Fatalf("runUpgrade release failed: %v", err)
	}

	// Verify fence is cleared and writers can run
	if maintenance.IsUpgradeHeld(maintDir) || backupWritersHeld() {
		t.Fatal("site must be unlocked after release")
	}
}

func TestUpgradeLockTimeout(t *testing.T) {
	allowUpgradeTestUser(t)

	maintDir := t.TempDir()
	dropInDir := t.TempDir()
	t.Setenv("URMAIL_MAINTENANCE_ROOT", maintDir)
	ctx := t.Context()

	// Acquire lock externally to simulate concurrent operation
	lock, err := maintenance.AcquireLock(maintDir, maintenance.DefaultLockName)
	if err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer func() { _ = maintenance.ReleaseLock(lock) }()

	fenceArgs := []string{
		"fence",
		"--timeout", "100ms",
		"--root-dir", maintDir,
		"--drop-in-dir", dropInDir,
	}
	start := time.Now()
	err = runUpgrade(ctx, fenceArgs)
	elapsed := time.Since(start)

	if !errors.Is(err, maintenance.ErrLockTimeout) && (err == nil || !strings.Contains(err.Error(), "timeout")) {
		t.Fatalf("expected lock timeout error, got: %v", err)
	}
	if elapsed < 90*time.Millisecond {
		t.Fatalf("expected timeout wait of at least 90ms, got: %v", elapsed)
	}
}

func TestUpgradeReleaseFaultInjectionRestorationIncomplete(t *testing.T) {
	allowUpgradeTestUser(t)

	maintDir := t.TempDir()
	dropInDir := t.TempDir()
	t.Setenv("URMAIL_MAINTENANCE_ROOT", maintDir)
	ctx := t.Context()

	// Simulate fence in place with active units to restart
	meta := maintenance.UpgradeFenceMeta{
		Version:   1,
		CreatedAt: time.Now().UTC(),
		Reason:    "fault injection release cleanup",
		Operator:  "test-operator",
		Units:     []string{"postfix.service", "dovecot.service"},
	}
	if err := maintenance.CreateUpgradeFence(maintDir, meta); err != nil {
		t.Fatalf("failed to create simulated fence: %v", err)
	}

	// Setup isolated test env file
	envFile := filepath.Join(maintDir, "urmail.env")
	_ = os.WriteFile(envFile, []byte("URMAIL_DATABASE_URL=postgres://app@localhost/urmail\nURMAIL_DATABASE_URL_PLATFORM=postgres://platform@localhost/urmail\nURMAIL_QUOTE_HMAC_KEY=test-key\nURMAIL_POLICY_ENABLED=true\n"), 0o600)
	t.Setenv("URMAIL_ENV_FILE", envFile)

	resetVerifier := setMigrationVerifierForTest(func(_ context.Context, _ string) error {
		return nil
	})
	defer resetVerifier()

	// Mock: start of dovecot fails, triggering restoreFenceOnFailure;
	// during restore, stopping dovecot also fails.
	reset := setBackupUnitActionForTest(func(_ context.Context, action, unit string) (bool, error) {
		if action == "start" && unit == "dovecot.service" {
			return false, errors.New("simulated start failure on dovecot")
		}
		if action == "stop" && unit == "dovecot.service" {
			return false, errors.New("simulated cleanup stop failure on dovecot")
		}
		if action == "state" {
			return false, nil
		}
		return true, nil
	})
	defer reset()

	releaseArgs := []string{
		"release",
		"--root-dir", maintDir,
		"--drop-in-dir", dropInDir,
		"--break-glass",
		"--ack-risk",
	}
	err := runUpgrade(ctx, releaseArgs)
	if err == nil {
		t.Fatal("expected runUpgrade release to fail")
	}

	// Because cleanup stop failed on dovecot, it must NOT claim RESTORED; it MUST state RESTORATION INCOMPLETE
	if strings.Contains(err.Error(), "verified RESTORED") {
		t.Fatalf("must NOT claim RESTORED when cleanup stop failed, got: %v", err)
	}
	if !strings.Contains(err.Error(), "RESTORATION INCOMPLETE") {
		t.Fatalf("expected RESTORATION INCOMPLETE in error message, got: %v", err)
	}
}

func TestUpgradeResumeStrictRejectionOnActiveWriters(t *testing.T) {
	allowUpgradeTestUser(t)

	maintDir := t.TempDir()
	t.Setenv("URMAIL_MAINTENANCE_ROOT", maintDir)
	ctx := t.Context()

	// Simulate active fence
	meta := maintenance.UpgradeFenceMeta{
		Version:   1,
		CreatedAt: time.Now().UTC(),
		Reason:    "active fence for resume test",
		Operator:  "test-operator",
	}
	if err := maintenance.CreateUpgradeFence(maintDir, meta); err != nil {
		t.Fatalf("CreateUpgradeFence failed: %v", err)
	}

	// Mock: postfix.service is unexpectedly active
	reset := setBackupUnitActionForTest(func(_ context.Context, action, unit string) (bool, error) {
		if action == "state" && unit == "postfix.service" {
			return true, nil
		}
		return false, nil
	})
	defer reset()

	resumeArgs := []string{
		"resume",
		"--root-dir", maintDir,
	}
	err := runUpgrade(ctx, resumeArgs)
	if err == nil {
		t.Fatal("expected runUpgrade resume to fail when writers are active")
	}
	if !strings.Contains(err.Error(), "concurrent writes detected") {
		t.Fatalf("expected concurrent writes detected error, got: %v", err)
	}

	// Verify fence is still held
	if !maintenance.IsUpgradeHeld(maintDir) {
		t.Fatal("upgrade fence must be retained after resume rejection")
	}
}

func TestUpgradeResumeRejectsReleaseFlag(t *testing.T) {
	allowUpgradeTestUser(t)

	maintDir := t.TempDir()
	t.Setenv("URMAIL_MAINTENANCE_ROOT", maintDir)
	ctx := t.Context()

	// Simulate active fence
	meta := maintenance.UpgradeFenceMeta{
		Version:   1,
		CreatedAt: time.Now().UTC(),
		Reason:    "active fence for resume test",
		Operator:  "test-operator",
	}
	if err := maintenance.CreateUpgradeFence(maintDir, meta); err != nil {
		t.Fatalf("CreateUpgradeFence failed: %v", err)
	}

	// Pass --release
	resumeArgs := []string{
		"resume",
		"--root-dir", maintDir,
		"--release",
	}
	err := runUpgrade(ctx, resumeArgs)
	if err == nil {
		t.Fatal("expected runUpgrade resume --release to be rejected")
	}
	if !strings.Contains(err.Error(), "automatic fence release is strictly prohibited") {
		t.Fatalf("expected automatic release prohibited error, got: %v", err)
	}

	// Verify fence is still held
	if !maintenance.IsUpgradeHeld(maintDir) {
		t.Fatal("upgrade fence must be retained")
	}
}
