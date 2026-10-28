package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
)

const backupMaintenanceRoot = "/var/lib/urmail-backup"
const backupReceiptName = "maintenance.json"

type backupOperationContext struct{}
type backupMaintenanceReceipt struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Units     []string  `json:"original_active_units"`
}
type backupUnitAction func(context.Context, string, string) (bool, error)

// backupWriterUnits excludes the independent job itself, unlike the restore
// allowlist which must also stop backup scheduling and the backup job.
func backupWriterUnits() []string {
	units := []string{}
	for _, unit := range restoreWriterUnits {
		if unit != "urmail-backup-auto.service" && unit != "urmail-backup-auto.timer" {
			units = append(units, unit)
		}
	}
	return units
}
func systemBackupUnit(ctx context.Context, action, unit string) (bool, error) {
	if action != "state" && action != "start" && action != "stop" {
		return false, errors.New("invalid backup unit action")
	}
	if action == "state" {
		output, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "--property=LoadState", "--property=ActiveState", unit).Output() // #nosec G204 -- fixed executable; unit comes from the fixed backup writer allowlist.
		if err != nil {
			return false, fmt.Errorf("inspect backup writer %s: %w", unit, err)
		}
		text := string(output)
		if strings.Contains(text, "LoadState=not-found") {
			return false, nil
		}
		if !strings.Contains(text, "LoadState=loaded") {
			return false, fmt.Errorf("backup writer %s has unknown load state", unit)
		}
		for _, state := range []string{"active", "activating", "reloading"} {
			if strings.Contains(text, "ActiveState="+state+"\n") {
				return true, nil
			}
		}
		if strings.Contains(text, "ActiveState=inactive") || strings.Contains(text, "ActiveState=failed") {
			return false, nil
		}
		return false, fmt.Errorf("backup writer %s has transitional state", unit)
	}
	err := exec.CommandContext(ctx, "/usr/bin/systemctl", action, unit).Run() // #nosec G204 -- action and units are allowlisted, never shell text.
	return false, err
}
func prepareBackupMaintenanceRoot(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := validatePrivateBackupDirectory(directory); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New("backup maintenance directory must be root-only 0700")
	}
	if err = syncBackupReceiptDirectory(directory); err != nil {
		return err
	}
	return syncBackupReceiptDirectory(filepath.Dir(directory))
}
func backupLock(directory, name string) (*os.File, error) {
	if err := prepareBackupMaintenanceRoot(directory); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(directory, name), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, errors.New("invalid root backup lock")
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("another backup or restore operation is running")
	}
	return file, nil
}
func syncBackupReceiptDirectory(directory string) error {
	dir, err := os.Open(directory) // #nosec G304 -- fixed root-owned receipt directory, validated before every operation.
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
func validateBackupMaintenanceReceipt(receipt backupMaintenanceReceipt) error {
	if receipt.Version != 1 || receipt.CreatedAt.IsZero() || len(receipt.Units) > len(backupWriterUnits()) {
		return errors.New("invalid backup maintenance receipt")
	}
	allowed := map[string]bool{}
	for _, unit := range backupWriterUnits() {
		allowed[unit] = true
	}
	seen := map[string]bool{}
	for _, unit := range receipt.Units {
		if !allowed[unit] || seen[unit] {
			return errors.New("backup receipt contains an unowned or duplicate writer")
		}
		seen[unit] = true
	}
	return nil
}
func readBackupMaintenanceReceipt(directory string) (backupMaintenanceReceipt, error) {
	var receipt backupMaintenanceReceipt
	fd, err := syscall.Open(filepath.Join(directory, backupReceiptName), syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return receipt, err
	}
	file := os.NewFile(uintptr(fd), backupReceiptName)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return receipt, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 16384 {
		return receipt, errors.New("invalid backup receipt owner, mode or size")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 16385))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&receipt); err != nil {
		return receipt, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return receipt, errors.New("invalid backup receipt trailing content")
	}
	return receipt, validateBackupMaintenanceReceipt(receipt)
}
func resumeBackupWritersLocked(ctx context.Context, directory string, action backupUnitAction) error {
	receipt, err := readBackupMaintenanceReceipt(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if backupWritersHeld() {
		return errors.New("restore quarantine prevents automatic backup writer restart; receipt retained")
	}
	failures := []error{}
	for i := len(receipt.Units) - 1; i >= 0; i-- {
		if backupWritersHeld() {
			return errors.New("maintenance hold appeared while resuming; receipt retained")
		}
		if _, err = action(ctx, "start", receipt.Units[i]); err != nil {
			failures = append(failures, fmt.Errorf("resume backup writer %s: %w", receipt.Units[i], err))
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if err = os.Remove(filepath.Join(directory, backupReceiptName)); err != nil {
		return err
	}
	return syncBackupReceiptDirectory(directory)
}
func pauseBackupWritersDurable(ctx context.Context, directory string, action backupUnitAction) (func() error, error) {
	lock, err := backupLock(directory, "maintenance.lock")
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = lock.Close()
		}
	}()
	if backupWritersHeld() {
		return nil, errors.New("restore quarantine prevents backup maintenance")
	}
	if err = resumeBackupWritersLocked(ctx, directory, action); err != nil {
		return nil, err
	}
	receipt := backupMaintenanceReceipt{Version: 1, CreatedAt: time.Now().UTC(), Units: []string{}}
	for _, unit := range backupWriterUnits() {
		active, e := action(ctx, "state", unit)
		if e != nil {
			return nil, e
		}
		if active {
			receipt.Units = append(receipt.Units, unit)
		}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(directory, backupReceiptName), syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), backupReceiptName)
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return nil, err
	}
	if err = syncBackupReceiptDirectory(directory); err != nil {
		return nil, err
	}
	resume := func() error {
		resumeCtx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		defer func() { _ = lock.Close() }()
		return resumeBackupWritersLocked(resumeCtx, directory, action)
	}
	for _, unit := range receipt.Units {
		if _, err = action(ctx, "stop", unit); err != nil {
			return nil, errors.Join(fmt.Errorf("stop backup writer %s: %w", unit, err), resume())
		}
	}
	failed = false
	return resume, nil
}
func runBackupAutoResume(ctx context.Context, args []string) error {
	if len(args) != 0 || os.Geteuid() != 0 {
		return errors.New("backup-auto-resume requires root and no arguments")
	}
	if _, err := os.Lstat(backupMaintenanceRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	lock, err := backupLock(backupMaintenanceRoot, "maintenance.lock")
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	return resumeBackupWritersLocked(ctx, backupMaintenanceRoot, systemBackupUnit)
}

// Any maintenance marker fails closed, including an unreadable or dangling
// marker. No stopped writer is restarted until the operator clears the hold.
func backupWritersHeld() bool {
	if config.RestoreQuarantined() || config.ParseBool(os.Getenv("URMAIL_WORKERS_HELD")) || config.ParseBool(os.Getenv("URMAIL_QUARANTINED")) {
		return true
	}
	if _, err := os.Lstat("/run/urmail-stable-worker-hold.env"); !errors.Is(err, os.ErrNotExist) {
		return true
	}
	matches, err := filepath.Glob("/var/lib/urmail-stable-upgrade-*.hold")
	return err != nil || len(matches) > 0
}
