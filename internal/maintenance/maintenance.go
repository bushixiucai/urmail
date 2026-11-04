// Package maintenance provides atomic filesystem locks, upgrade fences, and systemd write-stopping drop-in guards.
package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// DefaultMaintenanceRoot is the canonical directory for backup and upgrade locks and receipts.
	DefaultMaintenanceRoot = "/var/lib/urmail-backup"
	// DefaultLockName is the global operational exclusive file lock name.
	DefaultLockName = "operation.lock"
	// DefaultUpgradeHoldFile is the persistent upgrade fence marker filename.
	DefaultUpgradeHoldFile = "upgrade.hold"
	// DefaultUpgradeReceiptName is the receipt filename recording original active units for upgrade.
	DefaultUpgradeReceiptName = "upgrade_receipt.json"
	// BackupReceiptName is the maintenance receipt filename for backup pausing.
	BackupReceiptName = "maintenance.json"
	// BackupLockName is the lock filename for backup maintenance routines.
	BackupLockName = "maintenance.lock"
	// DefaultDropInDir is the systemd system configuration directory.
	DefaultDropInDir = "/etc/systemd/system"
	// DropInFileName is the drop-in configuration filename for writer fence guards.
	DropInFileName = "99-urmail-upgrade-fence.conf"
)

var (
	// ErrLockTimeout indicates that the lock could not be acquired within the specified timeout.
	ErrLockTimeout = errors.New("timeout acquiring maintenance lock")
	// ErrLockHeld indicates that the lock is currently held by another operation.
	ErrLockHeld = errors.New("another backup, restore or upgrade operation is running")
	// ErrFenceAlreadyExists indicates that an upgrade fence is already active.
	ErrFenceAlreadyExists = errors.New("upgrade fence already exists")
	// ErrFenceNotActive indicates that no upgrade fence is currently active.
	ErrFenceNotActive = errors.New("upgrade fence is not active")
	// ErrBackupMaintenanceActive indicates that an unrecovered backup maintenance receipt exists.
	ErrBackupMaintenanceActive = errors.New("unrecovered backup maintenance receipt exists")
)

// DefaultWriterUnits lists concrete activation and writer units. Stop activation
// sockets before services so PID 1 cannot keep ingress open or reactivate writers.
// Never include service templates here: only concrete instances can be stopped
// and recorded as originally active.
var DefaultWriterUnits = []string{
	"dovecot.socket",
	"urmail-backup-auto.timer",
	"urmail-backup-auto.service",
	"urmail-cert-renew.timer",
	"urmail-usage-sync.timer",
	"urmail-cert-renew.service",
	"urmail-usage-sync.service",
	"urmail-prometheus.service",
	"urmail-alertmanager.service",
	"nginx.service",
	"php8.1-fpm.service",
	"php8.2-fpm.service",
	"php8.3-fpm.service",
	"php8.4-fpm.service",
	"php8.5-fpm.service",
	"php8.6-fpm.service",
	"postfix.service",
	"postfix@-.service",
	"dovecot.service",
	"urmaild.service",
	"urmaild-agent.service",
}

// DropInGuardedUnits lists units to which drop-in fence guards are applied.
// Writers and daemon units are strictly guarded to prevent resurrection upon reboot.
var DropInGuardedUnits = []string{
	"dovecot.socket",
	// Guard the template as well as the distro default concrete instance, so
	// additional Postfix instances inherit the fence after daemon-reload/reboot.
	"postfix@.service",
	"postfix.service",
	"postfix@-.service",
	"dovecot.service",
	"nginx.service",
	"urmaild.service",
	"urmaild-agent.service",
	"urmail-usage-sync.service",
	"urmail-usage-sync.timer",
	"urmail-cert-renew.service",
	"urmail-cert-renew.timer",
	"urmail-backup-auto.service",
	"urmail-backup-auto.timer",
	"urmail-prometheus.service",
	"urmail-alertmanager.service",
	"php8.1-fpm.service",
	"php8.2-fpm.service",
	"php8.3-fpm.service",
	"php8.4-fpm.service",
	"php8.5-fpm.service",
	"php8.6-fpm.service",
}

// Root resolves the effective maintenance directory, respecting URMAIL_MAINTENANCE_ROOT.
func Root(directory string) string {
	if directory != "" {
		return directory
	}
	if env := os.Getenv("URMAIL_MAINTENANCE_ROOT"); env != "" {
		return env
	}
	return DefaultMaintenanceRoot
}

// LockHandle represents an acquired filesystem lock.
type LockHandle struct {
	file     *os.File
	path     string
	mu       sync.Mutex
	borrowed bool // never unlock a shared open-file description
}

// File returns the underlying file pointer.
func (h *LockHandle) File() *os.File {
	if h == nil {
		return nil
	}
	return h.file
}

// Path returns the path of the locked file.
func (h *LockHandle) Path() string {
	if h == nil {
		return ""
	}
	return h.path
}

// Close releases the lock and closes the file descriptor.
func (h *LockHandle) Close() error {
	return ReleaseLock(h)
}

// PrepareDirectory creates the directory with 0700 permissions and validates security boundaries.
func PrepareDirectory(directory string) error {
	directory = Root(directory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create maintenance directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("stat maintenance directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("maintenance directory must be a real directory")
	}
	if info.Mode().Perm() != 0o700 {
		if err = os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("chmod maintenance directory 0700: %w", err)
		}
		info, err = os.Lstat(directory)
		if err != nil {
			return fmt.Errorf("stat maintenance directory: %w", err)
		}
		if info.Mode().Perm() != 0o700 {
			return errors.New("maintenance directory must have 0700 permissions")
		}
	}
	if os.Geteuid() == 0 {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != 0 {
			return errors.New("maintenance directory must be owned by root")
		}
	}
	return SyncDirectory(directory)
}

// SyncDirectory calls fsync on the directory to ensure directory metadata persistence.
func SyncDirectory(directory string) error {
	dir, err := os.Open(directory) // #nosec G304 -- verified maintenance directory
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func validateLockFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("lock file must be a regular file with 0600 permissions")
	}
	if os.Geteuid() == 0 {
		if owner, ok := info.Sys().(*syscall.Stat_t); ok && owner.Uid != 0 {
			return errors.New("lock file must be owned by root")
		}
	}
	return nil
}

// AcquireLock attempts non-blocking exclusive flock on the specified lock file.
func AcquireLock(directory, name string) (*LockHandle, error) {
	return AcquireLockWithTimeout(context.Background(), directory, name, 0)
}

// AcquireLockWithTimeout attempts to acquire an exclusive flock on filepath.Join(directory, name)
// retrying until timeout expires or context is canceled.
func AcquireLockWithTimeout(ctx context.Context, directory, name string, timeout time.Duration) (*LockHandle, error) {
	directory = Root(directory)
	if name == "" {
		name = DefaultLockName
	}
	if err := PrepareDirectory(directory); err != nil {
		return nil, err
	}
	targetPath := filepath.Join(directory, name)
	if name == DefaultLockName && os.Getenv(InstallerLeaseEnv) != "" {
		return inheritInstallerLease(targetPath)
	}

	var deadline time.Time
	hasTimeout := timeout > 0
	if hasTimeout {
		deadline = time.Now().Add(timeout)
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		fd, err := syscall.Open(targetPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open lock file %s: %w", targetPath, err)
		}
		file := os.NewFile(uintptr(fd), name)

		if err = validateLockFile(file); err != nil {
			_ = file.Close()
			return nil, err
		}

		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &LockHandle{
				file: file,
				path: targetPath,
			}, nil
		}

		_ = file.Close()

		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("flock %s: %w", targetPath, err)
		}

		if !hasTimeout || time.Now().After(deadline) {
			if hasTimeout {
				return nil, ErrLockTimeout
			}
			return nil, ErrLockHeld
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ReleaseLock releases the exclusive flock and closes the file descriptor.
// It is idempotent and safe to call multiple times.
func ReleaseLock(handle *LockHandle) error {
	if handle == nil {
		return nil
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()

	if handle.file == nil {
		return nil
	}

	fd := int(handle.file.Fd())
	if !handle.borrowed {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
	}
	err := handle.file.Close()
	handle.file = nil
	return err
}

// UpgradeFenceMeta records metadata for the upgrade fence.
// Version 2 fences additionally carry the target schema generation; the
// companion receipt binds the exact hold bytes via HoldSHA256.
type UpgradeFenceMeta struct {
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	Reason        string    `json:"reason"`
	Operator      string    `json:"operator"`
	TargetVersion string    `json:"target_version,omitempty"`
	Generation    uint64    `json:"generation,omitempty"`
	Units         []string  `json:"original_active_units,omitempty"`
	DropIns       []string  `json:"guarded_units,omitempty"`
}

// UpgradeReceipt records original active units to restore upon upgrade release.
// Version 2 receipts bind the hold file bytes (HoldSHA256) and the schema
// generation so external verifiers can detect hold edits and generation drift.
type UpgradeReceipt struct {
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	HoldSHA256  string    `json:"hold_sha256,omitempty"`
	Generation  uint64    `json:"generation,omitempty"`
	Units       []string  `json:"original_active_units"`
	Reason      string    `json:"reason,omitempty"`
	UpgradeHeld bool      `json:"upgrade_held,omitempty"`
}

// FenceStatus reports current upgrade fence state and associated metadata.
type FenceStatus struct {
	Active        bool              `json:"active"`
	HoldPath      string            `json:"hold_path"`
	Meta          *UpgradeFenceMeta `json:"meta,omitempty"`
	Receipt       *UpgradeReceipt   `json:"receipt,omitempty"`
	DropIns       map[string]bool   `json:"drop_in_guards,omitempty"`
	OperationLock bool              `json:"operation_lock_held"`
}

// UpgradeHoldState distinguishes proven absence from corrupt/uninspectable state.
// An abnormal marker must never authorize guard cleanup or writer restart.
func UpgradeHoldState(directory string) (bool, error) {
	path := filepath.Join(Root(directory), DefaultUpgradeHoldFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("inspect upgrade hold: %w", err)
	}
	if !info.Mode().IsRegular() {
		return true, errors.New("upgrade hold is not a regular file; guards retained")
	}
	return true, nil
}

// IsUpgradeHeld fails closed for any marker or inspection error.
func IsUpgradeHeld(directory string) bool {
	held, err := UpgradeHoldState(directory)
	return held || err != nil
}

// CreateUpgradeFence writes upgrade.hold and upgrade_receipt.json atomically.
// It checks for unrecovered backup maintenance and acquires maintenance.lock.
func CreateUpgradeFence(directory string, meta UpgradeFenceMeta) error {
	directory = Root(directory)
	if err := PrepareDirectory(directory); err != nil {
		return err
	}

	holdPath := filepath.Join(directory, DefaultUpgradeHoldFile)
	if _, err := os.Lstat(holdPath); err == nil {
		return ErrFenceAlreadyExists
	}

	// Verify no unrecovered backup maintenance exists
	backupReceiptPath := filepath.Join(directory, BackupReceiptName)
	if bInfo, err := os.Lstat(backupReceiptPath); err == nil && bInfo.Mode().IsRegular() {
		return fmt.Errorf("%w at %s; run 'urmailctl backup-auto-resume' first", ErrBackupMaintenanceActive, backupReceiptPath)
	}

	// Acquire maintenance.lock to coordinate with backup maintenance
	maintLock, err := AcquireLockWithTimeout(context.Background(), directory, BackupLockName, 5*time.Second)
	if err != nil {
		return fmt.Errorf("acquire maintenance lock for upgrade fence: %w", err)
	}
	defer func() { _ = ReleaseLock(maintLock) }()

	// Double check backup receipt after acquiring maintenance lock
	if bInfo, err := os.Lstat(backupReceiptPath); err == nil && bInfo.Mode().IsRegular() {
		return fmt.Errorf("%w at %s; run 'urmailctl backup-auto-resume' first", ErrBackupMaintenanceActive, backupReceiptPath)
	}

	if meta.Version == 0 {
		meta.Version = 2
	}
	if meta.Version != 1 && meta.Version != 2 {
		return fmt.Errorf("unsupported upgrade fence version %d", meta.Version)
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now().UTC()
	}
	if meta.Operator == "" {
		meta.Operator = fmt.Sprintf("uid-%d", os.Geteuid())
	}
	if meta.Units == nil {
		meta.Units = []string{}
	}

	holdRaw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal upgrade fence meta: %w", err)
	}

	holdSum := sha256.Sum256(holdRaw)
	receipt := UpgradeReceipt{
		Version:     meta.Version,
		CreatedAt:   meta.CreatedAt,
		Units:       meta.Units,
		Reason:      meta.Reason,
		UpgradeHeld: true,
	}
	if meta.Version >= 2 {
		receipt.HoldSHA256 = hex.EncodeToString(holdSum[:])
		receipt.Generation = meta.Generation
	}
	receiptRaw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal upgrade receipt: %w", err)
	}

	// Write upgrade.hold atomically
	tmpHold := filepath.Join(directory, fmt.Sprintf(".%s.%d.tmp", DefaultUpgradeHoldFile, time.Now().UnixNano()))
	if err = writePrivateFile(tmpHold, holdRaw); err != nil {
		_ = os.Remove(tmpHold)
		return err
	}
	if err = os.Rename(tmpHold, holdPath); err != nil {
		_ = os.Remove(tmpHold)
		return fmt.Errorf("commit upgrade hold file: %w", err)
	}

	// Write upgrade_receipt.json atomically (independent from backup maintenance.json)
	receiptPath := filepath.Join(directory, DefaultUpgradeReceiptName)
	tmpReceipt := filepath.Join(directory, fmt.Sprintf(".%s.%d.tmp", DefaultUpgradeReceiptName, time.Now().UnixNano()))
	if err = writePrivateFile(tmpReceipt, receiptRaw); err != nil {
		_ = os.Remove(tmpReceipt)
		return err
	}
	if err = os.Rename(tmpReceipt, receiptPath); err != nil {
		_ = os.Remove(tmpReceipt)
		return fmt.Errorf("commit upgrade receipt: %w", err)
	}

	return SyncDirectory(directory)
}

func writePrivateFile(path string, data []byte) error {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("open private file %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

// RemoveUpgradeFence removes upgrade.hold and cleans up upgrade_receipt.json.
// It NEVER touches backup maintenance.json. It is idempotent.
func RemoveUpgradeFence(directory string) error {
	directory = Root(directory)
	holdPath := filepath.Join(directory, DefaultUpgradeHoldFile)
	if err := os.Remove(holdPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove upgrade hold: %w", err)
	}

	receiptPath := filepath.Join(directory, DefaultUpgradeReceiptName)
	if err := os.Remove(receiptPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove upgrade receipt: %w", err)
	}

	return SyncDirectory(directory)
}

// CheckFenceStatus inspects the directory for upgrade.hold and upgrade_receipt.json.
func CheckFenceStatus(directory string) (*FenceStatus, error) {
	directory = Root(directory)
	status := &FenceStatus{
		HoldPath: filepath.Join(directory, DefaultUpgradeHoldFile),
	}

	holdInfo, err := os.Lstat(status.HoldPath)
	if err == nil && holdInfo.Mode().IsRegular() {
		status.Active = true
		raw, readErr := readLimitedFile(status.HoldPath, 32768)
		if readErr == nil {
			var meta UpgradeFenceMeta
			if jsonErr := json.Unmarshal(raw, &meta); jsonErr == nil {
				status.Meta = &meta
			}
		}
	}

	receiptPath := filepath.Join(directory, DefaultUpgradeReceiptName)
	receiptInfo, err := os.Lstat(receiptPath)
	if err == nil && receiptInfo.Mode().IsRegular() {
		raw, readErr := readLimitedFile(receiptPath, 32768)
		if readErr == nil {
			var receipt UpgradeReceipt
			if jsonErr := json.Unmarshal(raw, &receipt); jsonErr == nil {
				status.Receipt = &receipt
			}
		}
	}

	lockPath := filepath.Join(directory, DefaultLockName)
	fd, lockErr := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW, 0o600)
	if lockErr == nil {
		file := os.NewFile(uintptr(fd), DefaultLockName)
		if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			status.OperationLock = true
		} else {
			_ = syscall.Flock(fd, syscall.LOCK_UN)
		}
		_ = file.Close()
	}

	return status, nil
}

func readLimitedFile(path string, maxBytes int64) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, maxBytes))
}

// ReadUpgradeFenceRaw returns the exact live upgrade hold and receipt bytes.
// Both files must exist as regular files; callers bind external attestations
// to these digests instead of trusting re-serialized JSON.
func ReadUpgradeFenceRaw(directory string) ([]byte, []byte, error) {
	directory = Root(directory)
	holdRaw, err := readLimitedFile(filepath.Join(directory, DefaultUpgradeHoldFile), 32768)
	if err != nil {
		return nil, nil, fmt.Errorf("read upgrade hold: %w", err)
	}
	receiptRaw, err := readLimitedFile(filepath.Join(directory, DefaultUpgradeReceiptName), 32768)
	if err != nil {
		return nil, nil, fmt.Errorf("read upgrade receipt: %w", err)
	}
	return holdRaw, receiptRaw, nil
}

// InstallDropInGuards installs drop-in unit files that refuse or prevent service startup
// while upgrade.hold exists. Files are written with fsync and directory sync.
func InstallDropInGuards(dropInBaseDir, holdFilePath string, units []string) ([]string, error) {
	if dropInBaseDir == "" {
		dropInBaseDir = DefaultDropInDir
	}
	if holdFilePath == "" {
		holdFilePath = filepath.Join(Root(""), DefaultUpgradeHoldFile)
	}
	if len(units) == 0 {
		units = DropInGuardedUnits
	}

	content := fmt.Sprintf("# Generated by urmailctl upgrade fence. DO NOT EDIT.\n"+
		"[Unit]\n"+
		"Description=urMail upgrade fence active (writes blocked)\n"+
		"ConditionPathExists=!%s\n",
		holdFilePath)

	installed := []string{}
	for _, unit := range units {
		unitContent := content
		// Socket/timer activation is fenced by [Unit] conditions. Only service
		// units accept [Service] and its defense-in-depth ExecStartPre.
		if strings.HasSuffix(unit, ".service") {
			unitContent += fmt.Sprintf("\n[Service]\nExecStartPre=/usr/bin/test ! -f %s\n", holdFilePath)
		}
		dropInDir := filepath.Join(dropInBaseDir, unit+".d")
		if err := os.MkdirAll(dropInDir, 0o755); err != nil {
			return installed, fmt.Errorf("create drop-in dir for %s: %w", unit, err)
		}
		confPath := filepath.Join(dropInDir, DropInFileName)
		tmpPath := filepath.Join(dropInDir, fmt.Sprintf(".%s.%d.tmp", DropInFileName, time.Now().UnixNano()))
		if err := writeFsyncFile(tmpPath, []byte(unitContent), 0o644); err != nil {
			_ = os.Remove(tmpPath)
			return installed, fmt.Errorf("write drop-in guard for %s: %w", unit, err)
		}
		if err := os.Rename(tmpPath, confPath); err != nil {
			_ = os.Remove(tmpPath)
			return installed, fmt.Errorf("commit drop-in guard for %s: %w", unit, err)
		}
		if err := SyncDirectory(dropInDir); err != nil {
			return installed, fmt.Errorf("fsync drop-in dir for %s: %w", unit, err)
		}
		installed = append(installed, unit)
	}
	if err := SyncDirectory(dropInBaseDir); err != nil {
		return installed, fmt.Errorf("fsync drop-in base dir: %w", err)
	}

	return installed, nil
}

func writeFsyncFile(path string, data []byte, perm os.FileMode) error {
	cleanPath := filepath.Clean(path)
	f, err := os.OpenFile(cleanPath, os.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC, perm) // #nosec G304 -- path is constructed internally within root-owned drop-in directories.
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

// RemoveDropInGuards removes drop-in unit files for the specified units.
// It is idempotent and safe to call when guards are already removed.
func RemoveDropInGuards(dropInBaseDir string, units []string) error {
	if dropInBaseDir == "" {
		dropInBaseDir = DefaultDropInDir
	}
	if len(units) == 0 {
		units = DropInGuardedUnits
	}

	var errs []error
	for _, unit := range units {
		confPath := filepath.Join(dropInBaseDir, unit+".d", DropInFileName)
		if err := os.Remove(confPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove drop-in guard for %s: %w", unit, err))
		}
		_ = os.Remove(filepath.Join(dropInBaseDir, unit+".d"))
	}
	return errors.Join(errs...)
}

// CheckDropInGuards checks which units have active drop-in fence guards installed.
func CheckDropInGuards(dropInBaseDir string, units []string) (map[string]bool, error) {
	if dropInBaseDir == "" {
		dropInBaseDir = DefaultDropInDir
	}
	if len(units) == 0 {
		units = DropInGuardedUnits
	}

	result := make(map[string]bool, len(units))
	for _, unit := range units {
		confPath := filepath.Join(dropInBaseDir, unit+".d", DropInFileName)
		info, err := os.Lstat(confPath)
		result[unit] = err == nil && info.Mode().IsRegular()
	}
	return result, nil
}

// SystemctlReload triggers a systemd daemon-reload.
// If systemctl is present, its return code must be checked.
func SystemctlReload(ctx context.Context) error {
	cmdPath, err := exec.LookPath("systemctl")
	if err != nil {
		return nil
	}
	if _, err = os.Lstat("/run/systemd/system"); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return exec.CommandContext(ctx, cmdPath, "daemon-reload").Run() // #nosec G204 -- systemctl is resolved via LookPath with fixed arguments.
}
