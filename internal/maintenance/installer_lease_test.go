package maintenance

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestBorrowInstallerLeaseRejectsForgeryAndPreservesExclusion(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireLock(dir, DefaultLockName)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	path := filepath.Join(dir, DefaultLockName)
	borrowed, err := borrowInstallerLease(path, int(lock.File().Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err = borrowed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = AcquireLock(dir, DefaultLockName); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("borrowed close released parent lock: %v", err)
	}
	fresh, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if forged, err := borrowInstallerLease(path, int(fresh.Fd())); err == nil {
		forged.Close()
		t.Fatal("accepted different open description owned by no lock")
	}
	other, err := os.OpenFile(filepath.Join(dir, "other"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err = syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if forged, err := borrowInstallerLease(path, int(other.Fd())); err == nil {
		forged.Close()
		t.Fatal("accepted unrelated locked inode")
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	if forged, err := borrowInstallerLease(path, int(fresh.Fd())); err == nil {
		forged.Close()
		t.Fatal("accepted unlocked descriptor")
	}
}

func TestInstallerLeaseEnvironmentCannotBypassLock(t *testing.T) {
	t.Setenv(InstallerLeaseEnv, "not-a-descriptor")
	if lock, err := AcquireLock(t.TempDir(), DefaultLockName); err == nil {
		lock.Close()
		t.Fatal("forged environment bypassed validation")
	}
	// Non-operation locks must never inherit the operation lease.
	lock, err := AcquireLock(t.TempDir(), BackupLockName)
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
}
