// DB-backed shared-message verdict engine.
//
// A milter edits one shared queue message, potentially delivered to unrelated
// tenants. It must never apply one recipient's lists or training to that shared
// message. Private sender policies run in Dovecot's recipient-scoped before
// script; feedback token statistics remain isolated and are not used here.

package mailspam

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// EngineConfig carries the deployment facts the pure engine must not fetch
// itself.
type EngineConfig struct {
	// TrustedAuthServIDs lists the authserv-ids whose Authentication-Results
	// headers are trusted. Must match the IDs the OpenDKIM/OpenDMARC milters
	// write (usually the MTA hostname); empty disables authentication
	// evidence entirely, so a misconfiguration degrades instead of gaming.
	TrustedAuthServIDs []string
	// LocalDomains lists hosted mail domains for the SASL outbound signal.
	LocalDomains []string
	// DenyActions enables rejection (default OFF; see MilterConfig).
	DenyActions bool
	// LookupTimeout bounds each policy lookup; a slow database must not stall
	// the SMTP conversation beyond what Postfix tolerates.
	LookupTimeout time.Duration
}

// withDefaults fills unset fields.
func (c EngineConfig) withDefaults() EngineConfig {
	if c.LookupTimeout <= 0 {
		c.LookupTimeout = 3 * time.Second
	}
	return c
}

// Engine is the DB-backed VerdictHandler.
type Engine struct {
	// Platform resolves envelope recipients (platform role, RLS bypass is
	// intentional and limited to the address->(tenant,mailbox) mapping).
	Platform *db.Pool
	// App is retained for compatibility; shared classification never reads private policy.
	App *db.Pool
	// Config carries deployment facts.
	Config EngineConfig
	// Logger receives one-line warnings for degraded verdicts.
	Logger interface {
		Printf(format string, args ...any)
	}
}

// Decide implements VerdictHandler.
func (e *Engine) Decide(ctx context.Context, req VerdictRequest) VerdictResponse {
	cfg := e.Config.withDefaults()
	classify := false

	if _, ok := e.firstLocalRecipient(ctx, req.Recipients, cfg); ok {
		classify = true
	}
	if !classify {
		// No envelope recipient resolves to an active local mailbox: this is
		// outbound or relayed traffic. It passes through untagged and is
		// never rejected (documented v1 scope).
		recordSession("external")
		return VerdictResponse{}
	}

	auth := AuthContext{
		SASLUser: req.SASLUser,
		// A matching authserv-id is not proof of provenance. Incoming
		// Authentication-Results can be forged; do not use message headers
		// as trusted positive or negative authentication evidence.
		Results:      nil,
		LocalDomains: cfg.LocalDomains,
	}
	verdict := Classify(Message{
		Headers:      req.Headers,
		BodySample:   req.BodySample,
		EnvelopeFrom: req.EnvelopeFrom,
	}, auth, ListSnapshot{})
	recordVerdict(verdict, false)

	response := VerdictResponse{InsertHeaders: VerdictHeaders(verdict)}
	if cfg.DenyActions && verdict.Class == ClassSpam && verdict.Score >= cfg.DenyScoreThresholdEngine() {
		response.Reject = true
	}
	return response
}

// DenyScoreThresholdEngine returns the configured deny threshold with the
// engine default applied (sender-list weight: only an explicit deny rule
// rejects, and only when DenyActions is enabled).
func (c EngineConfig) DenyScoreThresholdEngine() float64 {
	return WeightSenderList
}

// firstLocalRecipient resolves the first envelope recipient.
func (e *Engine) firstLocalRecipient(ctx context.Context, recipients []string, cfg EngineConfig) (store.PlatformGetMailboxByAddressRow, bool) {
	if e.Platform == nil {
		return store.PlatformGetMailboxByAddressRow{}, false
	}
	for _, raw := range recipients {
		address := strings.ToLower(strings.TrimSpace(raw))
		if address == "" {
			continue
		}
		lookupCtx, cancel := context.WithTimeout(ctx, cfg.LookupTimeout)
		row, err := store.New(e.Platform.Pool).PlatformGetMailboxByAddress(lookupCtx, address)
		cancel()
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			e.warn("mailspam: recipient lookup failed: %v", err)
			return store.PlatformGetMailboxByAddressRow{}, false
		}
		return row, true
	}
	return store.PlatformGetMailboxByAddressRow{}, false
}

func (e *Engine) warn(format string, args ...any) {
	if e.Logger != nil {
		e.Logger.Printf(format, args...)
		return
	}
	log.Printf(format, args...)
}
