// Package aliassenders maintains explicitly reviewed alias sender identities.
package aliassenders

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

// Controlled errors never wrap database details or private route values.
var (
	// ErrInput identifies malformed maintenance intent.
	ErrInput = errors.New("ALIAS_SENDERS_INVALID_INPUT")
	// ErrAuthority requires active platform operator authority.
	ErrAuthority = errors.New("ALIAS_SENDERS_AUTHORITY_REQUIRED")
	// ErrTarget rejects an unavailable sender or unsupported alias.
	ErrTarget = errors.New("ALIAS_SENDERS_TARGET_REJECTED")
	// ErrStale requires a fresh review of changed authoritative state.
	ErrStale = errors.New("ALIAS_SENDERS_PREVIEW_CHANGED")
	// ErrRejected reports an atomic transaction failure without private details.
	ErrRejected = errors.New("ALIAS_SENDERS_TRANSACTION_REJECTED")
	// ErrUnknown requires reconciliation after an uncertain commit acknowledgement.
	ErrUnknown = errors.New("ALIAS_SENDERS_COMMIT_UNKNOWN_DO_NOT_REPLAY")
)

// Intent requires an explicit list; an empty list revokes all sender grants.
type Intent struct {
	TenantID     int64    `json:"tenant_id"`
	ActorID      int64    `json:"actor_id"`
	AliasID      int64    `json:"alias_id"`
	Reason       string   `json:"reason"`
	SenderLogins []string `json:"sender_logins"`
}

func (Intent) String() string { return "<private alias sender intent>" }

// GoString redacts private operator intent in diagnostic formatting.
func (Intent) GoString() string { return "<private alias sender intent>" }

// Review is private operator material. Do not write it to ordinary stdout/logs.
type Review struct {
	Version int                                    `json:"version"`
	Intent  Intent                                 `json:"intent"`
	Alias   store.PlatformLockAliasSenderTargetRow `json:"alias"`
	Digest  string                                 `json:"digest"`
}

func (Review) String() string { return "<private alias sender review>" }

// GoString redacts route values in diagnostic formatting.
func (Review) GoString() string { return "<private alias sender review>" }

// Result contains only safe maintenance outcome metadata.
type Result struct {
	Code          string `json:"code"`
	Digest        string `json:"digest"`
	PreviousCount int    `json:"previous_count"`
	SelectedCount int    `json:"selected_count"`
}

func normalize(intent Intent) (Intent, error) {
	if intent.TenantID <= 0 || intent.ActorID <= 0 || intent.AliasID <= 0 || intent.SenderLogins == nil || len(intent.SenderLogins) > 100 {
		return Intent{}, ErrInput
	}
	intent.Reason = strings.TrimSpace(intent.Reason)
	if utf8.RuneCountInString(intent.Reason) < 20 || utf8.RuneCountInString(intent.Reason) > 2000 || strings.ContainsFunc(intent.Reason, unicode.IsControl) {
		return Intent{}, ErrInput
	}
	intent.SenderLogins = append([]string{}, intent.SenderLogins...)
	sort.Strings(intent.SenderLogins)
	for i, address := range intent.SenderLogins {
		parts := strings.Split(address, "@")
		if len(address) > 254 || address != strings.ToLower(address) || len(parts) != 2 || len(parts[0]) == 0 || len(parts[0]) > 64 || !validation.LocalPartRegex.MatchString(parts[0]) || strings.ContainsAny(parts[0], "*?\\/") || !validation.IsValidHostname(parts[1]) || (i > 0 && address == intent.SenderLogins[i-1]) {
			return Intent{}, ErrInput
		}
	}
	return intent, nil
}

func begin(ctx context.Context, pool *db.Pool, intent Intent) (pgx.Tx, Review, error) {
	if pool == nil || pool.Role() != db.RolePlatform {
		return nil, Review{}, ErrAuthority
	}
	intent, err := normalize(intent)
	if err != nil {
		return nil, Review{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, Review{}, ErrRejected
	}
	fail := func(e error) (pgx.Tx, Review, error) { rollback(tx); return nil, Review{}, e }
	q := store.New(tx)
	alias, err := q.PlatformLockAliasSenderTarget(ctx, store.PlatformLockAliasSenderTargetParams{ActorID: intent.ActorID, TenantID: intent.TenantID, AliasID: intent.AliasID})
	if err != nil {
		return fail(ErrAuthority)
	}
	if len(intent.SenderLogins) > 0 && (alias.Kind == "catchall" || alias.LocalPart == "") {
		return fail(ErrTarget)
	}
	addresses, err := q.PlatformLockAliasSenderMailboxes(ctx, store.PlatformLockAliasSenderMailboxesParams{TenantID: intent.TenantID, Addresses: intent.SenderLogins})
	if err != nil {
		return fail(ErrRejected)
	}
	if len(addresses) != len(intent.SenderLogins) {
		return fail(ErrTarget)
	}
	for i := range addresses {
		if addresses[i] != intent.SenderLogins[i] {
			return fail(ErrTarget)
		}
	}
	// Bind the full route and previous grants, including their stored order.
	review := Review{Version: 1, Intent: intent, Alias: alias}
	raw, err := json.Marshal(review)
	if err != nil {
		return fail(ErrRejected)
	}
	sum := sha256.Sum256(raw)
	review.Digest = hex.EncodeToString(sum[:])
	return tx, review, nil
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// Preview locks and reads authoritative state, without changing or auditing it.
func Preview(ctx context.Context, pool *db.Pool, intent Intent) (Review, error) {
	tx, review, err := begin(ctx, pool, intent)
	if err != nil {
		return Review{}, err
	}
	defer rollback(tx)
	return review, nil
}

// Apply rechecks the reviewed state and commits the grant change and audit once.
// A lost COMMIT acknowledgement is never automatically retried.
func Apply(ctx context.Context, pool *db.Pool, intent Intent, expected string) (Result, error) {
	return apply(ctx, pool, intent, expected, func(tx pgx.Tx) error { return tx.Commit(ctx) })
}

func apply(ctx context.Context, pool *db.Pool, intent Intent, expected string, commit func(pgx.Tx) error) (Result, error) {
	if len(expected) != 64 || strings.Trim(expected, "0123456789abcdef") != "" {
		return Result{}, ErrInput
	}
	tx, review, err := begin(ctx, pool, intent)
	if err != nil {
		return Result{}, err
	}
	defer rollback(tx)
	result := Result{Code: "ALIAS_SENDERS_PREVIEW", Digest: review.Digest, PreviousCount: len(review.Alias.SenderLogins), SelectedCount: len(review.Intent.SenderLogins)}
	if expected != review.Digest {
		return result, ErrStale
	}
	q := store.New(tx)
	n, err := q.PlatformSetAliasSenderLogins(ctx, store.PlatformSetAliasSenderLoginsParams{TenantID: review.Intent.TenantID, AliasID: review.Intent.AliasID, SenderLogins: review.Intent.SenderLogins})
	if err != nil || n != 1 {
		return result, ErrRejected
	}
	detail, _ := json.Marshal(struct {
		Digest   string `json:"review_digest"`
		Reason   string `json:"reason"`
		Previous int    `json:"previous_count"`
		Selected int    `json:"selected_count"`
		Status   int    `json:"status"`
		Outcome  string `json:"outcome"`
	}{review.Digest, review.Intent.Reason, result.PreviousCount, result.SelectedCount, 200, "succeeded"})
	_, err = q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: review.Intent.TenantID, Valid: true}, ActorUserID: pgtype.Int8{Int64: review.Intent.ActorID, Valid: true}, Action: "UPDATE alias.sender_logins", TargetType: pgtype.Text{String: "mailbox_aliases", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(review.Intent.AliasID, 10), Valid: true}, Detail: detail})
	if err != nil {
		return result, ErrRejected
	}
	if commit(tx) != nil {
		result.Code = "ALIAS_SENDERS_COMMIT_UNKNOWN_DO_NOT_REPLAY"
		return result, ErrUnknown
	}
	result.Code = "ALIAS_SENDERS_APPLIED"
	return result, nil
}
