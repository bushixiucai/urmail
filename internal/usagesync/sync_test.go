package usagesync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadMaildirQuotaBytesFromSharedVolume(t *testing.T) {
	root := t.TempDir()
	maildir := filepath.Join(root, "mail", "mailboxes", "example.test", "alice")
	if err := os.MkdirAll(maildir, 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := "1048576S,1000C\n4096 1\n2048 1\n-1024 -1\n"
	if err := os.WriteFile(filepath.Join(maildir, "maildirsize"), []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadMaildirQuotaBytes(root, "alice@example.test")
	if err != nil {
		t.Fatalf("ReadMaildirQuotaBytes: %v", err)
	}
	if got != 5120 {
		t.Fatalf("got %d bytes, want 5120", got)
	}

	// Reject path traversal / invalid identity.
	if _, err := ReadMaildirQuotaBytes(root, "../evil@example.test"); err == nil {
		t.Fatal("expected unsafe mailbox identity to fail")
	}

	// Verify ParseMaildirQuotaBytes directly.
	parsed, err := ParseMaildirQuotaBytes(strings.NewReader(ledger))
	if err != nil || parsed != 5120 {
		t.Fatalf("ParseMaildirQuotaBytes = (%d, %v), want (5120, nil)", parsed, err)
	}
}

func TestUsageSyncSchedulerRunsPeriodically(t *testing.T) {
	var runs atomic.Int32
	sched := &Scheduler{
		Interval: 15 * time.Millisecond,
		Clock:    time.Now,
		SyncFn: func(context.Context) (int, error) {
			runs.Add(1)
			return 2, nil
		},
	}
	stop := sched.Start(t.Context())
	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if runs.Load() < 2 {
		t.Fatalf("expected Scheduler to run at least twice, got %d", runs.Load())
	}
}
