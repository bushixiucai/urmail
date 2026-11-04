package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

var activeCheckpointVerifier = func(ctx context.Context, checkpoint string) error {
	output, err := exec.CommandContext(ctx, "/usr/bin/python3", "/usr/share/urmail/upgrade-checkpoint.py", "verify", checkpoint).CombinedOutput() // #nosec G204 -- fixed interpreter and packaged verifier; checkpoint is a positional argument, never shell code
	if err != nil {
		return fmt.Errorf("checkpoint verification failed: %w: %s", err, output)
	}
	return nil
}

type installerExitError struct {
	cause error
	code  int
}

func (e *installerExitError) Error() string {
	return fmt.Sprintf("installer exited %d (upgrade fence retained): %v", e.code, e.cause)
}
func (e *installerExitError) Unwrap() error { return e.cause }

func runUpgradeInstaller(ctx context.Context, args []string) error {
	// No test or environment override on this execution boundary.
	if os.Geteuid() != 0 {
		return errors.New("run-installer requires root")
	}
	flags := flag.NewFlagSet("upgrade-run-installer", flag.ContinueOnError)
	scriptPath := flags.String("installer", "", "explicit trusted absolute installer script")
	if err := flags.Parse(args); err != nil {
		return err
	}
	script, err := openTrustedInstaller(*scriptPath)
	if err != nil {
		return err
	}
	defer func() { _ = script.Close() }()
	// Nonblocking: a competing installer/backup/restore must be rejected.
	lock, err := maintenance.AcquireLock(currentMaintenanceRoot(), maintenance.DefaultLockName)
	if err != nil {
		return err
	}
	// Close only, NEVER LOCK_UN: if this process is terminated or a child
	// remains alive, its inherited description must continue excluding peers.
	defer func() { _ = lock.File().Close() }()
	// Bash reads the already validated open inode, not a replaceable pathname.
	cmdArgs := append([]string{"--noprofile", "--norc", "/dev/fd/4"}, flags.Args()...)
	cmd := exec.CommandContext(ctx, "/bin/bash", cmdArgs...) // #nosec G204 -- root-only explicit trusted script opened and validated above; fd4 pins its inode and remaining arguments follow the script
	cmd.ExtraFiles = []*os.File{lock.File(), script}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case maintenance.InstallerLeaseEnv, "BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS", "CDPATH", "GLOBIGNORE", "PATH":
			continue
		}
		if strings.HasPrefix(key, "BASH_FUNC_") {
			continue
		}
		cmd.Env = append(cmd.Env, value)
	}
	cmd.Env = append(cmd.Env, maintenance.InstallerLeaseEnv+"=3", "PATH=/usr/sbin:/usr/bin:/sbin:/bin")
	if err = cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			return &installerExitError{cause: err, code: exit.ExitCode()}
		}
		return fmt.Errorf("installer failed (upgrade fence retained): %w", err)
	}
	return nil // Never release the persistent fence, even on success.
}

func openTrustedInstaller(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("installer requires a clean absolute path")
	}
	// Root-owned sticky /tmp is allowed only above an immediately private,
	// root-owned staging directory; all other ancestors are non-writable.
	childInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	for current := path; ; current = filepath.Dir(current) {
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return nil, statErr
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("untrusted installer path component %s", current)
		}
		if current == path && !info.Mode().IsRegular() {
			return nil, errors.New("installer must be a regular file")
		}
		if current != path && !info.IsDir() {
			return nil, errors.New("installer ancestor must be a directory")
		}
		if info.Mode().Perm()&0o022 != 0 {
			if !info.IsDir() || info.Mode()&os.ModeSticky == 0 || !childInfo.IsDir() || childInfo.Mode().Perm()&0o077 != 0 {
				return nil, fmt.Errorf("writable installer path component %s", current)
			}
		}
		childInfo = info
		if current == "/" {
			break
		}
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	actual, err := file.Stat()
	expected, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !os.SameFile(actual, expected) {
		_ = file.Close()
		return nil, errors.New("installer changed during validation")
	}
	return file, nil
}
