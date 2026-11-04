package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCanonicalWriterActivationAndTemplateGuards(t *testing.T) {
	writers, guards := map[string]int{}, map[string]bool{}
	for index, unit := range DefaultWriterUnits {
		if _, duplicate := writers[unit]; duplicate {
			t.Fatalf("duplicate writer %s", unit)
		}
		writers[unit] = index
		if strings.Contains(unit, "@.") {
			t.Fatalf("template must not be a stop/restart target: %s", unit)
		}
	}
	for _, unit := range DropInGuardedUnits {
		if guards[unit] {
			t.Fatalf("duplicate guard %s", unit)
		}
		guards[unit] = true
	}
	for unit := range writers {
		if !guards[unit] {
			t.Fatalf("writer missing guard: %s", unit)
		}
	}
	for _, unit := range []string{"dovecot.socket", "postfix@-.service", "dovecot.service", "postfix.service"} {
		if _, ok := writers[unit]; !ok {
			t.Fatalf("missing concrete writer/activation unit %s", unit)
		}
	}
	if !guards["postfix@.service"] {
		t.Fatal("Postfix template must fence other instances")
	}
	for unit, index := range writers {
		if strings.HasSuffix(unit, ".socket") {
			for service, serviceIndex := range writers {
				if strings.HasSuffix(service, ".service") && index > serviceIndex {
					t.Fatalf("socket %s must stop before service %s", unit, service)
				}
			}
		}
	}
	dir := t.TempDir()
	if _, err := InstallDropInGuards(dir, "/var/lib/urmail-backup/upgrade.hold", DropInGuardedUnits); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []string{"dovecot.socket", "urmail-backup-auto.timer", "postfix@-.service", "postfix@.service"} {
		content, err := os.ReadFile(filepath.Join(dir, unit+".d", DropInFileName))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "ConditionPathExists=!/var/lib/urmail-backup/upgrade.hold") {
			t.Fatalf("missing activation guard for %s", unit)
		}
		if strings.HasSuffix(unit, ".service") != strings.Contains(string(content), "[Service]") {
			t.Fatalf("incorrect service-only section for %s: %s", unit, content)
		}
	}
}

func TestAcquireAndReleaseLock(t *testing.T) {
	dir := t.TempDir()
	lockName := "operation.lock"

	handle, err := AcquireLock(dir, lockName)
	if err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	if handle == nil || handle.File() == nil {
		t.Fatal("expected non-nil handle and file")
	}

	// Try acquiring again in the same or another call: should fail with ErrLockHeld
	second, err := AcquireLock(dir, lockName)
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld, got: %v (handle=%v)", err, second)
	}

	// Release lock
	if err = ReleaseLock(handle); err != nil {
		t.Fatalf("ReleaseLock failed: %v", err)
	}

	// Double release must be idempotent and return nil
	if err = ReleaseLock(handle); err != nil {
		t.Fatalf("subsequent ReleaseLock failed: %v", err)
	}

	// Now second acquire should succeed
	third, err := AcquireLock(dir, lockName)
	if err != nil {
		t.Fatalf("AcquireLock after release failed: %v", err)
	}
	defer third.Close()
}

func TestAcquireLockWithTimeout(t *testing.T) {
	dir := t.TempDir()
	lockName := "operation.lock"

	first, err := AcquireLock(dir, lockName)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	defer first.Close()

	// Second acquire with short timeout should time out
	ctx := context.Background()
	start := time.Now()
	_, err = AcquireLockWithTimeout(ctx, dir, lockName, 120*time.Millisecond)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("expected ErrLockTimeout, got: %v", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("timeout returned prematurely: %v", elapsed)
	}

	// Test lock acquisition after release within timeout
	var wg sync.WaitGroup
	wg.Add(1)

	var second *LockHandle
	var secondErr error

	go func() {
		defer wg.Done()
		second, secondErr = AcquireLockWithTimeout(ctx, dir, lockName, 2*time.Second)
	}()

	// Sleep briefly then release first lock
	time.Sleep(100 * time.Millisecond)
	if err = first.Close(); err != nil {
		t.Fatalf("first close failed: %v", err)
	}

	wg.Wait()
	if secondErr != nil {
		t.Fatalf("second acquire failed to get lock after release: %v", secondErr)
	}
	if second == nil {
		t.Fatal("expected non-nil second lock handle")
	}
	_ = second.Close()
}

func TestUpgradeFenceLifecycle(t *testing.T) {
	dir := t.TempDir()

	if IsUpgradeHeld(dir) {
		t.Fatal("expected fence not held initially")
	}

	status, err := CheckFenceStatus(dir)
	if err != nil {
		t.Fatalf("CheckFenceStatus failed: %v", err)
	}
	if status.Active {
		t.Fatal("expected status not active initially")
	}

	meta := UpgradeFenceMeta{
		Version:       1,
		CreatedAt:     time.Now().UTC(),
		Reason:        "test maintenance",
		Operator:      "test-suite",
		TargetVersion: "0.2.0",
		Units:         []string{"postfix.service", "dovecot.service"},
	}

	// Create fence
	if err = CreateUpgradeFence(dir, meta); err != nil {
		t.Fatalf("CreateUpgradeFence failed: %v", err)
	}

	if !IsUpgradeHeld(dir) {
		t.Fatal("expected IsUpgradeHeld to be true after creation")
	}

	// Duplicate create should return ErrFenceAlreadyExists
	if err = CreateUpgradeFence(dir, meta); !errors.Is(err, ErrFenceAlreadyExists) {
		t.Fatalf("expected ErrFenceAlreadyExists, got: %v", err)
	}

	// Verify status
	status, err = CheckFenceStatus(dir)
	if err != nil {
		t.Fatalf("CheckFenceStatus failed: %v", err)
	}
	if !status.Active {
		t.Fatal("expected status.Active to be true")
	}
	if status.Meta == nil || status.Meta.Reason != "test maintenance" {
		t.Fatalf("unexpected meta: %+v", status.Meta)
	}
	if status.Receipt == nil || len(status.Receipt.Units) != 2 {
		t.Fatalf("unexpected receipt: %+v", status.Receipt)
	}

	// Remove fence
	if err = RemoveUpgradeFence(dir); err != nil {
		t.Fatalf("RemoveUpgradeFence failed: %v", err)
	}

	if IsUpgradeHeld(dir) {
		t.Fatal("expected IsUpgradeHeld to be false after removal")
	}

	// Duplicate remove must be idempotent
	if err = RemoveUpgradeFence(dir); err != nil {
		t.Fatalf("subsequent RemoveUpgradeFence failed: %v", err)
	}

	status, err = CheckFenceStatus(dir)
	if err != nil {
		t.Fatalf("CheckFenceStatus failed: %v", err)
	}
	if status.Active {
		t.Fatal("expected status.Active to be false after removal")
	}
}

func TestDropInGuards(t *testing.T) {
	dropInDir := t.TempDir()
	holdFile := filepath.Join(t.TempDir(), "upgrade.hold")
	units := []string{"postfix.service", "dovecot.service", "nginx.service"}

	// Initially no guards
	status, err := CheckDropInGuards(dropInDir, units)
	if err != nil {
		t.Fatalf("CheckDropInGuards failed: %v", err)
	}
	for _, unit := range units {
		if status[unit] {
			t.Fatalf("expected unit %s not guarded initially", unit)
		}
	}

	// Install drop-ins
	installed, err := InstallDropInGuards(dropInDir, holdFile, units)
	if err != nil {
		t.Fatalf("InstallDropInGuards failed: %v", err)
	}
	if len(installed) != len(units) {
		t.Fatalf("expected %d installed units, got %d", len(units), len(installed))
	}

	// Verify drop-in contents and existence
	status, err = CheckDropInGuards(dropInDir, units)
	if err != nil {
		t.Fatalf("CheckDropInGuards failed: %v", err)
	}
	for _, unit := range units {
		if !status[unit] {
			t.Fatalf("expected unit %s to be guarded", unit)
		}
		content, readErr := os.ReadFile(filepath.Join(dropInDir, unit+".d", DropInFileName))
		if readErr != nil {
			t.Fatalf("read drop-in file for %s: %v", unit, readErr)
		}
		text := string(content)
		if !strings.Contains(text, "ConditionPathExists=!"+holdFile) {
			t.Fatalf("drop-in missing ConditionPathExists: %s", text)
		}
		if !strings.Contains(text, "ExecStartPre=/usr/bin/test ! -f "+holdFile) {
			t.Fatalf("drop-in missing ExecStartPre: %s", text)
		}
	}

	// Remove drop-in guards
	if err = RemoveDropInGuards(dropInDir, units); err != nil {
		t.Fatalf("RemoveDropInGuards failed: %v", err)
	}

	status, err = CheckDropInGuards(dropInDir, units)
	if err != nil {
		t.Fatalf("CheckDropInGuards failed: %v", err)
	}
	for _, unit := range units {
		if status[unit] {
			t.Fatalf("expected unit %s guard removed", unit)
		}
	}

	// Idempotent duplicate removal
	if err = RemoveDropInGuards(dropInDir, units); err != nil {
		t.Fatalf("subsequent RemoveDropInGuards failed: %v", err)
	}
}
