package main

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
)

// runSecrets migrates at most one bounded batch. The default reports counts
// only; neither mode prints secrets or silently creates a replacement key.
func runSecrets(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "compact-rejected-webhooks" {
		return runRejectedWebhookMaintenance(ctx, args[1:])
	}
	if len(args) == 0 || args[0] != "migrate-totp" {
		return errors.New("secrets requires migrate-totp [--apply] [--batch-size N]")
	}
	fs := flag.NewFlagSet("secrets migrate-totp", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "encrypt one batch of legacy TOTP seeds")
	batch := fs.Int("batch-size", 100, "maximum number of seeds per transaction (1-1000)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *batch < 1 || *batch > 1000 {
		return errors.New("batch-size must be between 1 and 1000 with no positional arguments")
	}
	if os.Geteuid() != 0 {
		return errors.New("TOTP secret maintenance requires root")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()
	q := store.New(pool)
	if !*apply {
		count, err := q.PlatformCountLegacyMFASecrets(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("legacy_totp_seeds=%d applied=false\n", count)
		return nil
	}
	box, err := secrets.Load(cfg.MasterKeyPath)
	if err != nil {
		return errors.New("a valid original master key is required; no seeds were changed")
	}
	changed, err := encryptLegacyTOTPBatch(ctx, pool, box, int32(*batch))
	if err != nil {
		return err
	}
	remaining, err := q.PlatformCountLegacyMFASecrets(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("encrypted_totp_seeds=%d remaining_legacy_totp_seeds=%d applied=true\n", changed, remaining)
	return nil
}

func runRejectedWebhookMaintenance(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("secrets compact-rejected-webhooks", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "compact one bounded batch of unverified historical webhook bodies")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || os.Geteuid() != 0 {
		return errors.New("webhook diagnostics maintenance requires root and no positional arguments")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()
	counts, err := payment.MaintainWebhookDiagnostics(ctx, pool, *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(counts)
}

func encryptLegacyTOTPBatch(ctx context.Context, pool *db.Pool, box *secrets.Box, batch int32) (int, error) {
	if box == nil || batch < 1 || batch > 1000 {
		return 0, errors.New("a valid key and bounded batch are required")
	}
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	rows, err := q.PlatformLockLegacyMFASecrets(ctx, batch)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, row := range rows {
		if !validLegacyTOTP(row.MfaSecret.String) {
			return 0, errors.New("invalid legacy TOTP seed; batch rolled back without printing secret values")
		}
		ciphertext, err := box.EncryptString(row.MfaSecret.String)
		if err != nil {
			return 0, errors.New("TOTP encryption failed; batch rolled back")
		}
		n, err := q.PlatformEncryptLegacyMFASecret(ctx, store.PlatformEncryptLegacyMFASecretParams{ID: row.ID, MfaSecret: row.MfaSecret, MfaSecret_2: pgtype.Text{String: ciphertext, Valid: true}})
		if err != nil || n != 1 {
			return 0, errors.New("TOTP seed changed concurrently or could not be updated; batch rolled back")
		}
		detail, _ := json.Marshal(map[string]string{"actor_type": "root_cli", "operation": "encrypt_legacy_totp"})
		if _, err := q.CreateAuditLog(ctx, store.CreateAuditLogParams{Action: "MAINTENANCE secrets.totp_migrated", TargetType: pgtype.Text{String: "users", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(row.ID, 10), Valid: true}, Detail: detail}); err != nil {
			return 0, err
		}
		changed++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return changed, nil
}

func validLegacyTOTP(seed string) bool {
	if len(seed) < 16 || len(seed) > 128 || strings.TrimSpace(seed) != seed || strings.HasPrefix(seed, "enc:") {
		return false
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(seed)
	return err == nil && len(decoded) >= 10
}
