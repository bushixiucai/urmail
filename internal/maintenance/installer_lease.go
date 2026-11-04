package maintenance

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// InstallerLeaseEnv locates a descriptor, never authorizes bypassing a lock.
const InstallerLeaseEnv = "URMAIL_UPGRADE_LOCK_FD"

func inheritInstallerLease(path string) (*LockHandle, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("installer lease requires root")
	}
	if os.Getenv(InstallerLeaseEnv) != "3" {
		return nil, errors.New("invalid installer lease descriptor (expected 3)")
	}
	return borrowInstallerLease(path, 3)
}

// CheckInstallerLease verifies an inherited operation lock without changing it.
func CheckInstallerLease(directory string) error {
	if os.Getenv(InstallerLeaseEnv) == "" {
		return errors.New("no installer lease")
	}
	lock, err := inheritInstallerLease(Root(directory) + "/" + DefaultLockName)
	if err != nil {
		return err
	}
	return lock.Close()
}

func borrowInstallerLease(path string, fd int) (*LockHandle, error) {
	dup, err := syscall.Dup(fd)
	if err != nil {
		return nil, fmt.Errorf("duplicate installer lease: %w", err)
	}
	syscall.CloseOnExec(dup)
	file := os.NewFile(uintptr(dup), "installer-operation-lease")
	fail := func(err error) (*LockHandle, error) { _ = file.Close(); return nil, err }
	if err = validateLockFile(file); err != nil {
		return fail(err)
	}
	probeFD, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fail(fmt.Errorf("open canonical installer lock: %w", err))
	}
	probe := os.NewFile(uintptr(probeFD), path)
	defer func() { _ = probe.Close() }()
	heldInfo, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	probeInfo, err := probe.Stat()
	if err != nil {
		return fail(err)
	}
	if !os.SameFile(heldInfo, probeInfo) {
		return fail(errors.New("installer lease inode differs from operation.lock"))
	}
	// Verify existing exclusion before touching the passed description's flock.
	err = syscall.Flock(probeFD, syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(probeFD, syscall.LOCK_UN)
		return fail(errors.New("installer lease is not already exclusively held"))
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		return fail(err)
	}
	// Another owner's flock cannot authenticate a freshly opened descriptor.
	if err = syscall.Flock(dup, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(fmt.Errorf("installer lease descriptor does not own exclusive flock: %w", err))
	}
	return &LockHandle{file: file, path: path, borrowed: true}, nil
}
