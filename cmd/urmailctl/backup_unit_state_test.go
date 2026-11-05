package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSystemctlStub replaces the systemctl executable with a script that
// emits the queued show outputs (one per invocation) and restores the original.
func writeSystemctlStub(t *testing.T, responses []string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "systemctl")
	counter := filepath.Join(dir, "invocations")
	script := "#!/bin/sh\ncount=$(cat " + counter + " 2>/dev/null || echo 0)\n" +
		"echo $((count + 1)) > " + counter + "\n" +
		"i=0\nfor out in " +
		`'` + strings.Join(responses, `' '`) + `'` +
		"; do if [ \"$i\" -eq \"$count\" ]; then printf '%s\\n' \"$out\"; exit 0; fi; i=$((i + 1)); done\n" +
		"printf 'ActiveState=inactive\\nLoadState=loaded\\n'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	prev := systemctlExecutable
	systemctlExecutable = path
	t.Cleanup(func() { systemctlExecutable = prev })
}

func TestBackupUnitStateSettlesTransitionalBeforeFailClosed(t *testing.T) {
	// A unit observed deactivating settles to inactive: not an error.
	writeSystemctlStub(t, []string{
		"LoadState=loaded\nActiveState=deactivating\n",
		"LoadState=loaded\nActiveState=inactive\n",
	})
	active, err := systemBackupUnit(context.Background(), "state", "postfix.service")
	if err != nil {
		t.Fatalf("transitional settle treated as failure: %v", err)
	}
	if active {
		t.Fatal("settled inactive unit reported active")
	}
}

func TestBackupUnitStateStillFailsClosedOnStuckTransitional(t *testing.T) {
	if testing.Short() {
		t.Skip("settle window too slow for short mode")
	}
	writeSystemctlStub(t, []string{
		"LoadState=loaded\nActiveState=deactivating\n",
		"LoadState=loaded\nActiveState=deactivating\n",
		"LoadState=loaded\nActiveState=deactivating\n",
	})
	prevAttempts := backupUnitStateSettleAttempts
	backupUnitStateSettleAttempts = 2
	t.Cleanup(func() { backupUnitStateSettleAttempts = prevAttempts })
	start := time.Now()
	_, err := systemBackupUnit(context.Background(), "state", "postfix.service")
	if err == nil || !strings.Contains(err.Error(), "transitional state") {
		t.Fatalf("stuck transitional state must fail closed, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("settle window unbounded: %v", elapsed)
	}
}
