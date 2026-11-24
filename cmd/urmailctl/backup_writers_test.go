package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// TestBackupWritersHeldDistinguishesUndeterminable: a real hold is a policy
// skip, but a read error or insufficient permission is an error the caller
// must surface, never a silent success.
func TestBackupWritersHeldDistinguishesUndeterminable(t *testing.T) {
	t.Setenv("URMAIL_MAINTENANCE_ROOT", t.TempDir())
	t.Setenv("URMAIL_WORKERS_HELD", "")
	t.Setenv("URMAIL_QUARANTINED", "")
	previousLstat, previousGlob := backupHoldLstat, backupHoldGlob
	t.Cleanup(func() { backupHoldLstat, backupHoldGlob = previousLstat, previousGlob })

	backupHoldLstat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	backupHoldGlob = func(string) ([]string, error) { return nil, nil }
	if held, err := backupWritersHeldChecked(); err != nil || held {
		t.Fatalf("normal state held=%v err=%v", held, err)
	}

	backupHoldLstat = func(string) (os.FileInfo, error) { return nil, fs.ErrPermission }
	if _, err := backupWritersHeldChecked(); err == nil || !strings.Contains(err.Error(), "stable worker hold") {
		t.Fatalf("permission error must surface, got %v", err)
	}
	if !backupWritersHeld() {
		t.Fatal("the conservative boolean must still skip on an undeterminable state")
	}

	backupHoldLstat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	backupHoldGlob = func(string) ([]string, error) { return nil, errors.New("glob exploded") }
	if _, err := backupWritersHeldChecked(); err == nil || !strings.Contains(err.Error(), "stable upgrade holds") {
		t.Fatalf("glob error must surface, got %v", err)
	}

	t.Setenv("URMAIL_WORKERS_HELD", "1")
	if held, err := backupWritersHeldChecked(); err != nil || !held {
		t.Fatalf("real hold held=%v err=%v", held, err)
	}
}

// TestBackupRefusesUndeterminableWriterState proves the manual backup path
// propagates the read error instead of skipping.
func TestBackupRefusesUndeterminableWriterState(t *testing.T) {
	allowUpgradeTestUser(t)
	t.Setenv("URMAIL_MAINTENANCE_ROOT", t.TempDir())
	t.Setenv("URMAIL_WORKERS_HELD", "")
	t.Setenv("URMAIL_QUARANTINED", "")
	previousLstat := backupHoldLstat
	t.Cleanup(func() { backupHoldLstat = previousLstat })
	backupHoldLstat = func(string) (os.FileInfo, error) { return nil, fs.ErrPermission }
	err := runBackup(context.Background(), []string{"--out-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "inspect stable worker hold") {
		t.Fatalf("backup must fail on an undeterminable writer state, got %v", err)
	}
}
