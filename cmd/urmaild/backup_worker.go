package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
)

// newBackupWorker assembles the OPS-001 worker from the environment.
// The privileged local backup runs through the agent op; encryption and
// upload happen in-process with Go libraries (age, net/http, sftp).
func newBackupWorker(cfg *config.Config, pool *db.Pool, recipient, offsiteURI string) (*backupauto.Worker, error) {
	if cfg.DevMode || cfg.Quarantined {
		return nil, fmt.Errorf("disabled in dev/quarantine mode")
	}
	creds := backupauto.Credentials{
		S3: backupauto.S3Credentials{
			AccessKeyID: os.Getenv("URMAIL_BACKUP_S3_ACCESS_KEY_ID"),
			SecretKey:   os.Getenv("URMAIL_BACKUP_S3_SECRET_KEY"),
			Region:      os.Getenv("URMAIL_BACKUP_S3_REGION"),
			Endpoint:    os.Getenv("URMAIL_BACKUP_S3_ENDPOINT"),
		},
		SFTP: backupauto.SFTPCredentials{
			Password:           os.Getenv("URMAIL_BACKUP_SFTP_PASSWORD"),
			PrivateKeyPath:     os.Getenv("URMAIL_BACKUP_SFTP_KEY"),
			HostKeyFingerprint: os.Getenv("URMAIL_BACKUP_SFTP_HOST_KEY"),
		},
	}
	store, err := backupauto.ParseDestination(offsiteURI, creds)
	if err != nil {
		return nil, err
	}
	agentClient := agent.NewClient(cfg.AgentSocket)
	runner := backupRunnerFunc(func(ctx context.Context, stamp string) ([]backupauto.LocalArtifact, string, bool, error) {
		// No caller-controlled parameters: the op runs one fixed command.
		if _, err := agentClient.Do(ctx, agentd.OpBackupLocalCreate, agentd.BackupLocalCreateParams{}); err != nil {
			return nil, "", false, fmt.Errorf("agent local backup: %w", err)
		}
		return backupauto.ReadSpoolArtifacts("/var/backups/urmail", stamp)
	})
	worker := &backupauto.Worker{
		Pool:       pool,
		Runner:     runner,
		Store:      store,
		Recipient:  recipient,
		OffsiteURI: offsiteURI,
		Interval:   envDuration("URMAIL_BACKUP_INTERVAL_HOURS", backupauto.DefaultInterval),
		Retention:  envRetention(),
	}
	return worker, nil
}

type backupRunnerFunc func(ctx context.Context, stamp string) ([]backupauto.LocalArtifact, string, bool, error)

func (f backupRunnerFunc) RunBackup(ctx context.Context, stamp string) ([]backupauto.LocalArtifact, string, bool, error) {
	return f(ctx, stamp)
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
