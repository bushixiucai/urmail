// Package miabimport imports reviewed legacy mail metadata without activating services.
package miabimport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/miadbinventory"
	"github.com/bushixiucai/urmail/internal/store"
)

var (
	// ErrAuthority rejects import attempts without reviewed operator authority.
	ErrAuthority = errors.New("LEGACY_IMPORT_OPERATOR_AUTHORITY_REQUIRED")
	// ErrSource requires a reviewed source snapshot.
	ErrSource = errors.New("LEGACY_IMPORT_REVIEWED_CORE_SOURCE_REQUIRED")
	// ErrTarget requires an empty, explicitly selected import target.
	ErrTarget = errors.New("LEGACY_IMPORT_EMPTY_TARGET_REQUIRED")
	// ErrRejected reports that the target failed an import guard.
	ErrRejected = errors.New("LEGACY_IMPORT_TARGET_REJECTED")
	// ErrUnknown forbids replay after an unconfirmed import commit.
	ErrUnknown   = errors.New("LEGACY_IMPORT_COMMIT_UNKNOWN_DO_NOT_REPLAY")
	proofPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// Options identifies an existing, explicitly selected tenant and accountable operator.
// No tenant, owner, platform authority, paid history or readiness is inferred.
type Options struct {
	TenantID  int64
	ActorID   int64
	TenantRef string
	Reason    string
}

// Result acknowledges core metadata only. It is never an activation certificate.
type Result struct {
	Phase              string   `json:"phase"`
	CommitOutcome      string   `json:"commit_outcome"`
	Applied            bool     `json:"applied"`
	CoreOnly           bool     `json:"core_only"`
	SievePending       bool     `json:"sieve_pending"`
	CutoverReady       bool     `json:"cutover_ready"`
	SnapshotSHA256     string   `json:"snapshot_sha256"`
	PrivateProofSHA256 string   `json:"private_proof_sha256"`
	Domains            int      `json:"domains"`
	Mailboxes          int      `json:"mailboxes"`
	Aliases            int      `json:"aliases"`
	PendingProofs      []string `json:"pending_proofs"`
}

// ApplyCore commits metadata, the preserved service grant and its audit together.
// A nonempty target is refused, including a completed or uncertain prior import.
// The caller must retain the private source artifact and complete all remaining
// Maildir, Sieve, groupware, DNS/TLS and protocol gates before any activation.
func ApplyCore(ctx context.Context, pool *db.Pool, source *miadbinventory.ReviewedSource, options Options) (Result, error) {
	result := Result{Phase: "source_preflight", CoreOnly: true, SievePending: true, CommitOutcome: "not_committed"}
	if pool == nil || pool.Role() != db.RolePlatform || options.TenantID <= 0 || options.ActorID <= 0 {
		return result, ErrAuthority
	}
	reason := strings.TrimSpace(options.Reason)
	if utf8.RuneCountInString(reason) < 20 || utf8.RuneCountInString(reason) > 2000 || strings.ContainsFunc(reason, unicode.IsControl) {
		return result, ErrAuthority
	}
	if source == nil || !proofPattern.MatchString(source.Fingerprint()) || !proofPattern.MatchString(source.PrivateProofSHA256()) || options.TenantRef == "" {
		return result, ErrSource
	}
	if !source.CoreOnly() || !source.SievePending() || source.ValidateUnchanged(ctx) != nil {
		return result, ErrSource
	}
	domains, mailboxes, aliases := source.Domains(), source.Mailboxes(), source.Aliases()
	if len(domains) == 0 || len(domains) > 1000 || len(mailboxes) == 0 || len(mailboxes) > 50000 || len(aliases) > 50000 {
		return result, ErrSource
	}
	for _, domain := range domains {
		if domain.TenantRef != options.TenantRef {
			return result, ErrSource
		}
	}
	result.PendingProofs = source.PendingProofs()
	result.SnapshotSHA256 = source.Fingerprint()
	result.PrivateProofSHA256 = source.PrivateProofSHA256()
	result.Domains = len(domains)
	result.Mailboxes = len(mailboxes)
	result.Aliases = len(aliases)
	result.Phase = "target_transaction"
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return result, ErrRejected
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	q := store.New(tx)
	// The same advisory lock serializes quota allocation and subscription grants.
	if q.PlatformLockBillingTenant(ctx, options.TenantID) != nil {
		return result, ErrRejected
	}
	if _, err = q.PlatformLockLegacyImportTarget(ctx, store.PlatformLockLegacyImportTargetParams{TenantID: options.TenantID, ActorID: options.ActorID}); err != nil {
		return result, ErrAuthority
	}
	facts, err := q.PlatformLegacyImportTargetFacts(ctx, options.TenantID)
	if err != nil {
		return result, ErrRejected
	}
	if facts.Domains != 0 || facts.Mailboxes != 0 || facts.Aliases != 0 || facts.Imports != 0 {
		return result, ErrTarget
	}
	result.Phase = "domains"
	domainIDs := make(map[string]int64, len(domains))
	for _, domain := range domains {
		token := make([]byte, 32)
		if _, err = rand.Read(token); err != nil {
			return result, ErrRejected
		}
		domainID, createErr := q.PlatformCreateLegacyDomain(ctx, store.PlatformCreateLegacyDomainParams{TenantID: options.TenantID, Fqdn: domain.FQDN, VerificationToken: hex.EncodeToString(token)})
		if createErr != nil {
			return result, ErrRejected
		}
		domainIDs[domain.FQDN] = domainID
	}
	result.Phase = "mailboxes"
	for _, mailbox := range mailboxes {
		domainID := domainIDs[mailbox.FQDN]
		if domainID == 0 || mailbox.QuotaBytes < -1 || mailbox.QuotaBytes == 0 {
			return result, ErrSource
		}
		if _, err = q.PlatformCreateLegacyMailbox(ctx, store.PlatformCreateLegacyMailboxParams{TenantID: options.TenantID, DomainID: domainID, LocalPart: mailbox.LocalPart, Address: mailbox.Address, PasswordHash: mailbox.PasswordHash, QuotaBytes: mailbox.QuotaBytes}); err != nil {
			return result, ErrRejected
		}
	}
	result.Phase = "aliases"
	for _, alias := range aliases {
		domainID := domainIDs[alias.FQDN]
		if domainID == 0 {
			return result, ErrSource
		}
		logins := alias.SenderLogins
		if logins == nil {
			logins = []string{}
		}
		if _, err = q.PlatformCreateLegacyAlias(ctx, store.PlatformCreateLegacyAliasParams{TenantID: options.TenantID, DomainID: domainID, LocalPart: alias.LocalPart, Target: alias.Target, SenderLogins: logins}); err != nil {
			return result, ErrRejected
		}
	}
	result.Phase = "audit"
	auditResult := result
	auditResult.Phase = "committed"
	auditResult.Applied = true
	auditResult.CommitOutcome = "committed"
	detail, _ := json.Marshal(struct {
		Result
		Reason  string `json:"reason"`
		Status  int    `json:"status"`
		Outcome string `json:"outcome"`
	}{auditResult, reason, 200, "succeeded"})
	if _, err = q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: options.TenantID, Valid: true}, ActorUserID: pgtype.Int8{Int64: options.ActorID, Valid: true}, Action: "IMPORT legacy.mail", TargetType: pgtype.Text{String: "tenant", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(options.TenantID, 10), Valid: true}, Detail: detail}); err != nil {
		return result, ErrRejected
	}
	if source.ValidateUnchanged(ctx) != nil {
		return result, ErrSource
	}
	// A lost COMMIT acknowledgement must be reconciled from the target, not retried.
	result.Phase = "commit"
	if tx.Commit(ctx) != nil {
		result.CommitOutcome = "unknown"
		return result, ErrUnknown
	}
	result.Applied = true
	result.Phase = "committed"
	result.CommitOutcome = "committed"
	return result, nil
}
