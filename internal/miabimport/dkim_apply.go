package miabimport

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// ErrDKIMTarget rejects incomplete locks, prior keys, or unexpected domain state.
var ErrDKIMTarget = errors.New("LEGACY_DKIM_EMPTY_PENDING_DOMAINS_REQUIRED")

// DKIMResult acknowledges exact private-key preservation only. It never marks
// DNS ready, publishes TXT records, or claims cutover.
type DKIMResult struct {
	Phase              string `json:"phase"`
	CommitOutcome      string `json:"commit_outcome"`
	Applied            bool   `json:"applied"`
	CoreOnly           bool   `json:"core_only"`
	CutoverReady       bool   `json:"cutover_ready"`
	Domains            int    `json:"domains"`
	Selector           string `json:"selector"`
	PublicFingerprint  string `json:"public_fingerprint_sha256"`
	PrivateProofSHA256 string `json:"private_proof_sha256"`
	CoreProofSHA256    string `json:"core_proof_sha256"`
}

// ApplyReviewedDKIM stores the exact source PEM on every locked pending domain.
// Domains must already come from a committed core import bound to the same proof.
func ApplyReviewedDKIM(ctx context.Context, pool *db.Pool, dkim *ReviewedDKIM, options Options) (DKIMResult, error) {
	result := DKIMResult{Phase: "source_preflight", CoreOnly: true, CommitOutcome: "not_committed"}
	if pool == nil || pool.Role() != db.RolePlatform || options.TenantID <= 0 || options.ActorID <= 0 {
		return result, ErrAuthority
	}
	reason := strings.TrimSpace(options.Reason)
	if utf8.RuneCountInString(reason) < 20 || utf8.RuneCountInString(reason) > 2000 || strings.ContainsFunc(reason, unicode.IsControl) {
		return result, ErrAuthority
	}
	if dkim == nil || dkim.source == nil || dkim.DomainCount() == 0 || dkim.ValidateUnchanged(ctx) != nil {
		return result, ErrDKIMSource
	}
	if !proofPattern.MatchString(dkim.PrivateProofSHA256()) || !proofPattern.MatchString(dkim.source.PrivateProofSHA256()) {
		return result, ErrDKIMSource
	}
	names := make([]string, 0, len(dkim.domains))
	selector := ""
	for _, domain := range dkim.domains {
		if domain.fqdn == "" || domain.selector == "" {
			return result, ErrDKIMSource
		}
		if selector == "" {
			selector = domain.selector
		} else if domain.selector != selector {
			return result, ErrDKIMSource
		}
		names = append(names, domain.fqdn)
	}
	result.Domains = len(names)
	result.Selector = selector
	result.PublicFingerprint = dkim.PublicFingerprint()
	result.PrivateProofSHA256 = dkim.PrivateProofSHA256()
	result.CoreProofSHA256 = dkim.source.PrivateProofSHA256()
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
	if _, err = q.PlatformLockLegacyImportTarget(ctx, store.PlatformLockLegacyImportTargetParams{TenantID: options.TenantID, ActorID: options.ActorID}); err != nil {
		return result, ErrAuthority
	}
	locked, err := q.PlatformLockLegacyDKIMDomains(ctx, store.PlatformLockLegacyDKIMDomainsParams{
		TenantID:         options.TenantID,
		DomainNames:      names,
		CoreProofSha256:  result.CoreProofSHA256,
	})
	if err != nil {
		return result, ErrRejected
	}
	if len(locked) != len(names) {
		return result, ErrDKIMTarget
	}
	result.Phase = "persist_exact_private_key"
	byName := make(map[string]store.PlatformLockLegacyDKIMDomainsRow, len(locked))
	for _, row := range locked {
		if row.Status != "pending_dns" || row.ProvisioningState != "pending" || row.DkimPrivateKey.Valid {
			return result, ErrDKIMTarget
		}
		byName[row.Fqdn] = row
	}
	for _, domain := range dkim.domains {
		row, ok := byName[domain.fqdn]
		if !ok || row.DkimSelector != domain.selector {
			return result, ErrDKIMTarget
		}
		updated, setErr := q.PlatformSetLegacyDKIM(ctx, store.PlatformSetLegacyDKIMParams{
			Selector:   domain.selector,
			PrivateKey: dkim.privatePEM,
			TenantID:   options.TenantID,
			DomainID:   row.ID,
		})
		if setErr != nil || updated != 1 {
			return result, ErrRejected
		}
	}
	result.Phase = "audit"
	audit := result
	audit.Phase = "committed"
	audit.Applied = true
	audit.CommitOutcome = "committed"
	detail, _ := json.Marshal(struct {
		DKIMResult
		Reason  string `json:"reason"`
		Status  int    `json:"status"`
		Outcome string `json:"outcome"`
	}{audit, reason, 200, "succeeded"})
	if _, err = q.CreateAuditLog(ctx, store.CreateAuditLogParams{
		TenantID:    pgtype.Int8{Int64: options.TenantID, Valid: true},
		ActorUserID: pgtype.Int8{Int64: options.ActorID, Valid: true},
		Action:      "IMPORT legacy.dkim",
		TargetType:  pgtype.Text{String: "tenant", Valid: true},
		TargetID:    pgtype.Text{String: strconv.FormatInt(options.TenantID, 10), Valid: true},
		Detail:      detail,
	}); err != nil {
		return result, ErrRejected
	}
	if dkim.ValidateUnchanged(ctx) != nil {
		return result, ErrDKIMSource
	}
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
