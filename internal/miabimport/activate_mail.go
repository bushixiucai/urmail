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

// ErrActivateTarget rejects missing import proofs or unexpected domain rows.
var ErrActivateTarget = errors.New("LEGACY_MAIL_ACTIVATE_DELEGATED_PENDING_REQUIRED") //nolint:revive // stable operator-facing sentinel

// ActivateMailResult marks mail routing readiness for delegated imports only.
// It never claims cutover_ready, never flips mode away from delegated, and
// never enables managed DNS/NSD emission.
type ActivateMailResult struct {
	Phase                string `json:"phase"`
	CommitOutcome        string `json:"commit_outcome"`
	Applied              bool   `json:"applied"`
	CoreOnly             bool   `json:"core_only"`
	CutoverReady         bool   `json:"cutover_ready"`
	Domains              int    `json:"domains"`
	ExpectedDomains      int    `json:"expected_domains"`
	CoreProofSHA256      string `json:"core_proof_sha256"`
	ManagedDNSEmitted    bool   `json:"managed_dns_emitted"`
	ModeRemainsDelegated bool   `json:"mode_remains_delegated"`
}

// ActivateLegacyDelegatedMail promotes pending_dns+pending delegated domains to
// active+ready so Postfix/Dovecot snapshots include mailboxes, while keeping
// mode=delegated and dns_explicit=false so NSD stays external/preserved.
func ActivateLegacyDelegatedMail(ctx context.Context, pool *db.Pool, options Options, coreProofSHA256 string, expectedDomains int) (ActivateMailResult, error) {
	result := ActivateMailResult{
		Phase:                "preflight",
		CoreOnly:             true,
		CommitOutcome:        "not_committed",
		ModeRemainsDelegated: true,
		ManagedDNSEmitted:    false,
		ExpectedDomains:      expectedDomains,
		CoreProofSHA256:      strings.ToLower(strings.TrimSpace(coreProofSHA256)),
	}
	if pool == nil || pool.Role() != db.RolePlatform || options.TenantID <= 0 || options.ActorID <= 0 || expectedDomains <= 0 {
		return result, ErrAuthority
	}
	reason := strings.TrimSpace(options.Reason)
	if utf8.RuneCountInString(reason) < 20 || utf8.RuneCountInString(reason) > 2000 || strings.ContainsFunc(reason, unicode.IsControl) {
		return result, ErrAuthority
	}
	if !proofPattern.MatchString(result.CoreProofSHA256) {
		return result, ErrSource
	}
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
	result.Phase = "activate_delegated_mail"
	updated, err := q.PlatformActivateLegacyDelegatedMail(ctx, store.PlatformActivateLegacyDelegatedMailParams{
		TenantID:        options.TenantID,
		CoreProofSha256: result.CoreProofSHA256,
	})
	if err != nil {
		return result, ErrRejected
	}
	if int(updated) != expectedDomains {
		return result, ErrActivateTarget
	}
	var drifted int
	if err = tx.QueryRow(ctx, `
SELECT count(*)::int FROM domains
WHERE tenant_id=$1 AND (
 mode <> 'delegated' OR dns_explicit OR status <> 'active' OR provisioning_state <> 'ready'
 OR verified_at IS NULL OR dkim_private_key IS NULL OR dkim_private_key = ''
)`, options.TenantID).Scan(&drifted); err != nil || drifted != 0 {
		return result, ErrActivateTarget
	}
	result.Domains = int(updated)
	result.Phase = "audit"
	audit := result
	audit.Phase = "committed"
	audit.Applied = true
	audit.CommitOutcome = "committed"
	detail, _ := json.Marshal(struct {
		ActivateMailResult
		Reason  string `json:"reason"`
		Status  int    `json:"status"`
		Outcome string `json:"outcome"`
	}{audit, reason, 200, "succeeded"})
	if _, err = q.CreateAuditLog(ctx, store.CreateAuditLogParams{
		TenantID:    pgtype.Int8{Int64: options.TenantID, Valid: true},
		ActorUserID: pgtype.Int8{Int64: options.ActorID, Valid: true},
		Action:      "IMPORT legacy.activate-mail",
		TargetType:  pgtype.Text{String: "tenant", Valid: true},
		TargetID:    pgtype.Text{String: strconv.FormatInt(options.TenantID, 10), Valid: true},
		Detail:      detail,
	}); err != nil {
		return result, ErrRejected
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
