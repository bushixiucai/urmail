package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

const recoveryStateDirectory = "/var/lib/urmail/domain-recovery"

// recoveryProof is an upgrade credential, never a second domain registry.
// Its token must also match the canonical PG row before any result is stored.
type recoveryProof struct {
	TenantID  int64     `json:"tenant_id"`
	DomainID  int64     `json:"domain_id"`
	FQDN      string    `json:"fqdn"`
	Token     string    `json:"token"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (p recoveryProof) validate(now time.Time, tenantID, domainID int64, fqdn, token string) error {
	if p.TenantID != tenantID || p.DomainID != domainID || p.FQDN != fqdn || p.Token != token || len(p.Token) != 64 {
		return errors.New("ownership proof does not match the canonical domain; issue a fresh challenge")
	}
	if p.IssuedAt.After(now) || !p.ExpiresAt.After(now) || !p.ExpiresAt.After(p.IssuedAt) || p.ExpiresAt.Sub(p.IssuedAt) > 7*24*time.Hour {
		return errors.New("ownership proof expired or has invalid timestamps; issue a fresh challenge")
	}
	return nil
}

func runDomainRecovery(ctx context.Context, args []string) error {
	if len(args) == 0 || (args[0] != "challenge" && args[0] != "verify") {
		return errors.New("domain requires challenge or verify")
	}
	if os.Geteuid() != 0 {
		return errors.New("domain recovery must run as root")
	}
	fs := flag.NewFlagSet("domain "+args[0], flag.ContinueOnError)
	tenantID := fs.Int64("tenant-id", 0, "canonical tenant ID")
	domainID := fs.Int64("domain-id", 0, "canonical domain ID")
	stateDir := recoveryStateDirectory
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *tenantID <= 0 || *domainID <= 0 {
		return errors.New("positive --tenant-id and --domain-id are required")
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
	q := store.New(pool.Pool)
	domain, err := q.PlatformGetDomainForRecovery(ctx, store.PlatformGetDomainForRecoveryParams{TenantID: *tenantID, ID: *domainID})
	if err != nil {
		return fmt.Errorf("read canonical recovery domain: %w", err)
	}
	if err = os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if err = validatePrivateBackupDirectory(stateDir); err != nil {
		return err
	}
	info, err := os.Stat(stateDir)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New("proof directory must have mode 0700")
	}
	name := strconv.FormatInt(domain.ID, 10) + ".json"
	path := filepath.Join(stateDir, name)
	proofRoot, err := os.OpenRoot(stateDir)
	if err != nil {
		return err
	}
	defer func() { _ = proofRoot.Close() }()
	if args[0] == "challenge" {
		var random [32]byte
		if _, err = rand.Read(random[:]); err != nil {
			return err
		}
		now := time.Now().UTC()
		proof := recoveryProof{TenantID: *tenantID, DomainID: *domainID, FQDN: domain.Fqdn, Token: hex.EncodeToString(random[:]), IssuedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)}
		raw, marshalErr := json.Marshal(proof)
		if marshalErr != nil {
			return marshalErr
		}
		// Atomic replacement revokes a previous credential without following its
		// destination symlink. The canonical token is changed in the same DB tx
		// as the audit; a file/DB mismatch always fails closed on verify.
		file, createErr := os.CreateTemp(stateDir, ".domain-proof-")
		if createErr != nil {
			return createErr
		}
		temporary := file.Name()
		defer func() { _ = os.Remove(temporary) }()
		if err = file.Chmod(0o600); err == nil {
			_, err = file.Write(raw)
		}
		if err == nil {
			err = file.Sync()
		}
		err = errors.Join(err, file.Close())
		if err != nil {
			return err
		}
		tx, txErr := pool.PlatformTx(ctx)
		if txErr != nil {
			return txErr
		}
		defer func() { _ = tx.Rollback(ctx) }()
		queries := store.New(tx)
		changed, changeErr := queries.PlatformRotateDomainRecoveryToken(ctx, store.PlatformRotateDomainRecoveryTokenParams{TenantID: *tenantID, ID: *domainID, VerificationToken: proof.Token, Fqdn: domain.Fqdn})
		if changeErr != nil {
			return changeErr
		}
		if changed != 1 {
			return errors.New("canonical domain changed; retry")
		}
		if err = recoveryAudit(ctx, queries, *tenantID, *domainID, "domain.recovery.challenge"); err != nil {
			return err
		}
		if err = proofRoot.Rename(filepath.Base(temporary), name); err != nil {
			return err
		}
		dir, openErr := os.Open(stateDir)
		if openErr != nil {
			return openErr
		}
		syncErr := dir.Sync()
		closeErr := dir.Close()
		if syncErr != nil || closeErr != nil {
			return errors.Join(syncErr, closeErr)
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"domain_id": *domainID, "tenant_id": *tenantID, "verification_name": "_urmail-verify." + domain.Fqdn, "verification_value": proof.Token, "expires_at": proof.ExpiresAt})
	}
	if err = agentd.ValidateRootEnvironment(path); err != nil {
		return err
	}
	file, err := proofRoot.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	var proof recoveryProof
	if err = json.NewDecoder(io.LimitReader(file, 4096)).Decode(&proof); err != nil {
		return err
	}
	if proof.TenantID != *tenantID || proof.DomainID != *domainID {
		return errors.New("proof identity does not match the requested recovery object")
	}
	if err = confirmDomainRecovery(ctx, pool, proof, net.DefaultResolver.LookupTXT); err != nil {
		return err
	}

	if err = proofRoot.Remove(name); err != nil {
		return fmt.Errorf("domain verified; remove consumed private proof: %w", err)
	}
	fmt.Printf("domain %d verified independently; rerun migration preflight and configuration readiness\n", *domainID)
	return nil
}

func confirmDomainRecovery(ctx context.Context, pool *db.Pool, proof recoveryProof, lookupTXT func(context.Context, string) ([]string, error)) error {
	domain, err := store.New(pool.Pool).PlatformGetDomainForRecovery(ctx, store.PlatformGetDomainForRecoveryParams{TenantID: proof.TenantID, ID: proof.DomainID})
	if err != nil {
		return err
	}
	if err = proof.validate(time.Now().UTC(), proof.TenantID, proof.DomainID, domain.Fqdn, domain.VerificationToken); err != nil {
		return err
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	records, err := lookupTXT(lookupCtx, "_urmail-verify."+domain.Fqdn)
	if err != nil {
		return fmt.Errorf("independent DNS proof: %w", err)
	}
	matched := false
	for _, record := range records {
		if record == proof.Token {
			matched = true
			break
		}
	}
	if !matched {
		return errors.New("DNS ownership proof did not match; no domain state changed")
	}
	if err = proof.validate(time.Now().UTC(), proof.TenantID, proof.DomainID, domain.Fqdn, domain.VerificationToken); err != nil {
		return err
	}
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := store.New(tx)
	changed, err := queries.PlatformConfirmDomainRecovery(ctx, store.PlatformConfirmDomainRecoveryParams{TenantID: proof.TenantID, ID: proof.DomainID, Fqdn: domain.Fqdn, VerificationToken: proof.Token})
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("challenge was revoked or domain changed during DNS verification")
	}
	if err = recoveryAudit(ctx, queries, proof.TenantID, proof.DomainID, "domain.recovery.verified"); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func recoveryAudit(ctx context.Context, q *store.Queries, tenantID, domainID int64, action string) error {
	_, err := q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tenantID, Valid: true}, Action: action, TargetType: pgtype.Text{String: "domain", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(domainID, 10), Valid: true}, Detail: []byte(`{"actor_type":"root_operator","source":"upgrade_recovery"}`)})
	return err
}
