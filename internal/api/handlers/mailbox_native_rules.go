package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/sieve"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// MailboxRulesStore reads and writes only the authenticated mailbox's own
// managed Sieve script, with the same compare-and-swap the bridge uses.
type MailboxRulesStore interface {
	Sieve(ctx context.Context, tenantID, mailboxID int64) (script, mode string, err error)
	SaveSieve(ctx context.Context, tenantID, mailboxID int64, script, expectedScript string) (bool, error)
}

// StoreMailboxRules adapts the managed-sieve platform queries.
type StoreMailboxRules struct {
	Pool *db.Pool
}

// Sieve returns the current script and its mode; an unset script is "".
func (s StoreMailboxRules) Sieve(ctx context.Context, tenantID, mailboxID int64) (string, string, error) {
	if s.Pool == nil {
		return "", "", pgx.ErrNoRows
	}
	row, err := store.New(s.Pool).PlatformGetMailboxSieve(ctx, store.PlatformGetMailboxSieveParams{TenantID: tenantID, ID: mailboxID})
	if err != nil {
		return "", "", err
	}
	return row.SieveScript.String, row.SieveMode, nil
}

// SaveSieve persists the desired script only when the stored script still
// equals expectedScript.
func (s StoreMailboxRules) SaveSieve(ctx context.Context, tenantID, mailboxID int64, script, expectedScript string) (bool, error) {
	if s.Pool == nil {
		return false, pgx.ErrNoRows
	}
	n, err := store.New(s.Pool).PlatformSetMailboxSieve(ctx, store.PlatformSetMailboxSieveParams{
		TenantID:       tenantID,
		ID:             mailboxID,
		SieveScript:    pgtype.Text{String: script, Valid: true},
		ExpectedScript: expectedScript,
	})
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

type mailboxRulesRequest struct {
	Rules           sieve.Rules `json:"rules"`
	Confirmed       bool        `json:"confirmed"`
	ExpectedVersion string      `json:"expected_version"`
}

func (h *MailboxNativeHandler) rulesReady(c *gin.Context) (webmail.Session, bool) {
	if !h.requireEnabled(c) || h.Rules == nil {
		authError(c, http.StatusServiceUnavailable, "SIEVE_DISABLED")
		return webmail.Session{}, false
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return webmail.Session{}, false
	}
	return session, true
}

// GetRules returns the mailbox's active managed Sieve program as structured
// rules. Legacy ManageSieve-owned scripts stay read-only-by-owner and are
// never decoded here.
func (h *MailboxNativeHandler) GetRules(c *gin.Context) {
	session, ok := h.rulesReady(c)
	if !ok {
		return
	}
	script, mode, err := h.Rules.Sieve(c.Request.Context(), session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if mode == "legacy_v80" {
		c.JSON(http.StatusOK, gin.H{"state": "legacy_managesieve", "rules": sieve.Rules{}, "version": sieve.Digest(script)})
		return
	}
	if mode != "managed" {
		authError(c, http.StatusConflict, "SIEVE_SOURCE_INVALID")
		return
	}
	rules := sieve.Rules{}
	if script != "" {
		rules, err = sieve.Decode(script)
		if err != nil {
			authError(c, http.StatusConflict, "SIEVE_SOURCE_INVALID")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"rules": rules, "version": sieve.Digest(script), "state": "active"})
}

// PutRules compiles, confirms and activates structured rules through the
// existing managed-sieve pipeline. Dangerous modes (legacy scripts, mode
// flips) stay admin/agent-gated and are never reachable from this path.
func (h *MailboxNativeHandler) PutRules(c *gin.Context) {
	if h.Sieve == nil {
		authError(c, 503, "SIEVE_DISABLED")
		return
	}
	session, ok := h.rulesReady(c)
	if !ok {
		return
	}
	if c.Request.ContentLength > 65536 {
		authError(c, http.StatusRequestEntityTooLarge, "SIEVE_RULES_TOO_LARGE")
		return
	}
	var req mailboxRulesRequest
	if err := c.ShouldBindJSON(&req); err != nil || !req.Confirmed {
		authError(c, http.StatusBadRequest, "SIEVE_CONFIRMATION_REQUIRED")
		return
	}
	script, mode, err := h.Rules.Sieve(c.Request.Context(), session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if mode != "managed" {
		authError(c, http.StatusConflict, "SIEVE_LEGACY_MANAGESIEVE_REQUIRED")
		return
	}
	if len(req.ExpectedVersion) != 64 || req.ExpectedVersion != sieve.Digest(script) {
		authError(c, http.StatusConflict, "SIEVE_VERSION_CONFLICT")
		return
	}
	compiled, err := sieve.Compile(req.Rules)
	if err != nil {
		authError(c, http.StatusBadRequest, "SIEVE_RULES_INVALID")
		return
	}
	changed, err := h.Rules.SaveSieve(c.Request.Context(), session.TenantID, session.MailboxID, compiled, script)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if !changed {
		authError(c, http.StatusConflict, "SIEVE_VERSION_CONFLICT")
		return
	}
	if h.Sieve != nil {
		if err = h.Sieve.ActivateSieve(c.Request.Context(), session.TenantID, session.MailboxID, sieve.Digest(compiled)); err != nil {
			authError(c, http.StatusBadGateway, "SIEVE_ACTIVATION_FAILED")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"saved": true, "state": "active", "version": sieve.Digest(compiled)})
}
