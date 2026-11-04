package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
)

const automaticBackupSpool = "/var/backups/urmail"

type rootBackupRunner struct{}

func (rootBackupRunner) RunBackup(ctx context.Context, stamp string) ([]backupauto.LocalArtifact, string, bool, error) {
	if err := runBackup(ctx, []string{"--include-mail", "--maintenance", "--stamp", stamp, "--out-dir", automaticBackupSpool}); err != nil {
		return nil, "", false, err
	}
	return backupauto.ReadSpoolArtifacts(automaticBackupSpool, stamp)
}

func (rootBackupRunner) RunBackupManifest(ctx context.Context, stamp string) (*backupauto.Manifest, []backupauto.LocalArtifact, error) {
	if err := runBackup(ctx, []string{"--include-mail", "--maintenance", "--stamp", stamp, "--out-dir", automaticBackupSpool}); err != nil {
		return nil, nil, err
	}
	return backupauto.ReadSpool(automaticBackupSpool, stamp)
}
func runBackupAuto(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 || len(args) != 0 {
		return errors.New("backup-auto requires root and no arguments")
	}
	if backupWritersHeld() {
		fmt.Println("automatic backup disabled by restore or upgrade hold")
		return nil
	}
	lock, err := backupLock(currentMaintenanceRoot(), "operation.lock")
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if backupWritersHeld() {
		fmt.Println("automatic backup disabled by restore or upgrade hold")
		return nil
	}
	ctx = context.WithValue(ctx, backupOperationContext{}, true)
	if err = runBackupAutoResume(ctx, nil); err != nil {
		return err
	}
	values, err := config.LoadEnvFile(os.Getenv("URMAIL_ENV_FILE"))
	if err != nil {
		return err
	}
	get := func(name string) string {
		if value, ok := os.LookupEnv(name); ok {
			return value
		}
		return values[name]
	}
	// This one nonsecret root-only setting selects the strictly validated
	// Docker PostgreSQL adapter when invoking the ordinary backup implementation.
	if container := get("URMAIL_BACKUP_POSTGRES_CONTAINER"); container != "" {
		if err = os.Setenv("URMAIL_BACKUP_POSTGRES_CONTAINER", container); err != nil {
			return err
		}
	}
	recipient, uri := get("URMAIL_BACKUP_AGE_RECIPIENT"), get("URMAIL_BACKUP_OFFSITE_URI")
	if recipient == "" && uri == "" {
		fmt.Println("automatic backup disabled: recipient and offsite destination are not configured")
		return nil
	}
	if recipient == "" || uri == "" {
		return errors.New("automatic backup requires both age recipient and offsite destination; no writers stopped")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.BackgroundWorkersAllowed() {
		fmt.Println("automatic backup disabled in development, maintenance hold or restore quarantine")
		return nil
	}
	if _, err = backupauto.ParseRecipient(recipient); err != nil {
		return errors.New("invalid automatic backup age public recipient")
	}
	creds := backupauto.Credentials{
		S3:   backupauto.S3Credentials{AccessKeyID: get("URMAIL_BACKUP_S3_ACCESS_KEY_ID"), SecretKey: get("URMAIL_BACKUP_S3_SECRET_KEY"), Region: get("URMAIL_BACKUP_S3_REGION"), Endpoint: get("URMAIL_BACKUP_S3_ENDPOINT")},
		SFTP: backupauto.SFTPCredentials{Password: get("URMAIL_BACKUP_SFTP_PASSWORD"), PrivateKeyPath: get("URMAIL_BACKUP_SFTP_KEY"), HostKeyFingerprint: get("URMAIL_BACKUP_SFTP_HOST_KEY")},
	}
	if strings.HasPrefix(uri, "sftp://") && (creds.SFTP.HostKeyFingerprint == "" || (creds.SFTP.Password == "" && creds.SFTP.PrivateKeyPath == "")) {
		return errors.New("automatic SFTP backup requires authentication and a pinned SSH host fingerprint")
	}
	if creds.SFTP.PrivateKeyPath != "" && strings.HasPrefix(uri, "sftp://") {
		keyPath := filepath.Clean(creds.SFTP.PrivateKeyPath)

		if err = validatePrivateBackupDirectory(filepath.Dir(keyPath)); err != nil {
			return err
		}
		if err = validateRootBackupKey(keyPath); err != nil {
			return err
		}
	}
	store, err := backupauto.ParseDestination(uri, creds)
	if err != nil {
		return errors.New("invalid automatic backup destination or credentials")
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()
	// Holding the root operation flock proves no earlier root backup process is
	// alive. Preserve interrupted rows as failed facts before the next due check.
	if _, err = (&backupauto.StateRecorder{Pool: pool}).StaleRunning(ctx, 0, time.Now()); err != nil {
		return err
	}
	worker := backupauto.Worker{Pool: pool, Runner: rootBackupRunner{}, Store: store, Recipient: recipient, OffsiteURI: uri, Interval: envDuration("URMAIL_BACKUP_INTERVAL_HOURS", backupauto.DefaultInterval), Retention: envRetention()}
	return worker.RunOnce(ctx)
}

func envDuration(name string, fallback time.Duration) time.Duration {
	if raw := os.Getenv(name); raw != "" {
		if hours, err := strconv.Atoi(raw); err == nil && hours > 0 {
			return time.Duration(hours) * time.Hour
		}
	}
	return fallback
}

func envRetention() backupauto.RetentionPolicy {
	get := func(name string, fallback int) int {
		if raw := os.Getenv(name); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
				return n
			}
		}
		return fallback
	}
	return backupauto.RetentionPolicy{
		KeepDaily:   get("URMAIL_BACKUP_KEEP_DAILY", 7),
		KeepWeekly:  get("URMAIL_BACKUP_KEEP_WEEKLY", 4),
		KeepMonthly: get("URMAIL_BACKUP_KEEP_MONTHLY", 6),
	}
}

func validateRootBackupKey(path string) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "backup-sftp-key")
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 16384 {
		return errors.New("automatic SFTP key must be a root-owned private 0600 regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil {
		return err
	}
	if _, err = ssh.ParsePrivateKey(raw); err != nil {
		return errors.New("automatic SFTP private key cannot be parsed")
	}
	return nil
}
