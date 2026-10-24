package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/sieve"
	"github.com/bushixiucai/urmail/internal/store"
)

const sieveCookie = "urmail_mailbox_sieve"
const sieveCookiePath = "/api/v1/mailbox/sieve"

// SieveBridgeHandler authorizes the independent mailbox rule editor.
type SieveBridgeHandler struct {
	Pool      *db.Pool
	Key       []byte
	Activator SieveActivator
}

func (h *SieveBridgeHandler) csrf(token string) string {
	mac := hmac.New(sha256.New, h.Key)
	_, _ = mac.Write([]byte("sieve-csrf:" + token))
	return hex.EncodeToString(mac.Sum(nil))
}
func (h *SieveBridgeHandler) enabled(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	if h.Pool == nil || len(h.Key) < 32 {
		authError(c, 503, "SIEVE_DISABLED")
		return false
	}
	return true
}

// Exchange accepts the one-time ticket only in a same-origin POST body. The
// resulting credential cannot authenticate any panel or platform API.
func (h *SieveBridgeHandler) Exchange(c *gin.Context) {
	var auditTenant, auditMailbox int64
	defer func() { h.failedAudit(c, auditTenant, auditMailbox) }()
	if !h.enabled(c) {
		return
	}
	origin, err := url.Parse(c.GetHeader("Origin"))
	if err != nil {
		authError(c, 403, "UNAUTHORIZED")
		return
	}
	loopback := strings.HasPrefix(origin.Host, "127.0.0.1:") || origin.Host == "localhost" || strings.HasPrefix(origin.Host, "localhost:")
	permitted := origin.Host == c.Request.Host && (origin.Scheme == "https" || (origin.Scheme == "http" && loopback))
	if !permitted {
		authError(c, 403, "UNAUTHORIZED")
		return
	}
	if c.Request.ContentLength > 4096 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	source := c.PostForm("ticket")
	if len(source) != 64 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	principal, err := q.PlatformConsumeMailboxTicket(c, store.PlatformConsumeMailboxTicketParams{TokenHash: mailboxprincipal.HashToken(source), Scope: "sieve"})
	if err != nil {
		mailboxAuthorizationError(c, err)
		return
	}
	auditTenant, auditMailbox = principal.TenantID, principal.MailboxID
	token, hash, err := mailboxprincipal.NewToken()
	if err != nil {
		authError(c, 503, "SIEVE_DISABLED")
		return
	}
	if err = q.PlatformCreateSieveBridge(c, store.PlatformCreateSieveBridgeParams{TokenHash: hash, SourceTicket: mailboxprincipal.HashToken(source)}); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if !recordSieveAudit(c, q, principal.TenantID, principal.MailboxID, "sieve.bridge.exchanged") {
		return
	}
	if tx.Commit(c) != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(sieveCookie, token, 1800, sieveCookiePath, "", origin.Scheme == "https", true)
	c.Redirect(http.StatusSeeOther, "/mailbox/sieve")
}

func recordSieveAudit(c *gin.Context, q *store.Queries, tid, mid int64, action string) bool {
	detail, _ := json.Marshal(gin.H{"actor_type": "mailbox", "mailbox_id": mid, "status": 200, "outcome": "succeeded"})
	if _, err := q.CreateAuditLog(c, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tid, Valid: true}, Action: action, TargetType: pgtype.Text{String: "mailbox", Valid: true}, Detail: detail}); err != nil {
		middleware.RecordAuditFailure(true)
		authError(c, 503, "AUDIT_UNAVAILABLE")
		return false
	}
	return true
}

// Session reads the policy under a scoped mailbox credential.
func (h *SieveBridgeHandler) Session(c *gin.Context) { h.rules(c, false) }

// Save persists explicitly confirmed structured rules.
func (h *SieveBridgeHandler) Save(c *gin.Context) { h.rules(c, true) }

func (h *SieveBridgeHandler) rules(c *gin.Context, write bool) {
	var auditTenant, auditMailbox int64
	defer func() { h.failedAudit(c, auditTenant, auditMailbox) }()
	if !h.enabled(c) {
		return
	}
	token, err := c.Cookie(sieveCookie)
	if err != nil || len(token) != 64 {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	if write && !hmac.Equal([]byte(h.csrf(token)), []byte(c.GetHeader("X-Urmail-Sieve-CSRF"))) {
		authError(c, 403, "CSRF_INVALID")
		return
	}
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	principal, err := q.PlatformGetSieveBridge(c, mailboxprincipal.HashToken(token))
	if err != nil {
		mailboxAuthorizationError(c, err)
		return
	}
	auditTenant, auditMailbox = principal.TenantID, principal.MailboxID
	mailbox, err := q.PlatformGetMailboxSieve(c, store.PlatformGetMailboxSieveParams{TenantID: principal.TenantID, ID: principal.MailboxID})
	if err != nil {
		mailboxAuthorizationError(c, err)
		return
	}
	rules := sieve.Rules{}
	script := ""
	if write {
		var input struct {
			Rules           sieve.Rules `json:"rules"`
			Confirmed       bool        `json:"confirmed"`
			ExpectedVersion string      `json:"expected_version"`
		}
		if c.Request.ContentLength > 65536 || c.ShouldBindJSON(&input) != nil || !input.Confirmed {
			authError(c, 400, "SIEVE_CONFIRMATION_REQUIRED")
			return
		}
		if len(input.ExpectedVersion) != 64 || input.ExpectedVersion != sieve.Digest(mailbox.SieveScript.String) {
			authError(c, 409, "SIEVE_VERSION_CONFLICT")
			return
		}
		rules = input.Rules
		script, err = sieve.Compile(rules)
		if err != nil {
			authError(c, 400, "SIEVE_RULES_INVALID")
			return
		}
		if h.Activator == nil {
			authError(c, 503, "SIEVE_DISABLED")
			return
		}
		changed, saveErr := q.PlatformSetMailboxSieve(c, store.PlatformSetMailboxSieveParams{TenantID: principal.TenantID, ID: principal.MailboxID, SieveScript: pgtype.Text{String: script, Valid: true}, ExpectedScript: mailbox.SieveScript.String})
		if saveErr == nil && changed != 1 {
			authError(c, 409, "SIEVE_VERSION_CONFLICT")
			return
		}
		if saveErr != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
	} else if mailbox.SieveScript.Valid && mailbox.SieveScript.String != "" {
		script = mailbox.SieveScript.String
		rules, err = sieve.Decode(mailbox.SieveScript.String)
		if err != nil {
			authError(c, 409, "SIEVE_SOURCE_INVALID")
			return
		}
	}
	action := "sieve.read"
	if write {
		action = "sieve.desired.saved"
	}
	if !recordSieveAudit(c, q, principal.TenantID, principal.MailboxID, action) {
		return
	}
	if tx.Commit(c) != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	state := "desired"
	if write {
		applyCtx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		if h.Activator.ActivateSieve(applyCtx, principal.TenantID, principal.MailboxID, sieve.Digest(script)) != nil {
			authError(c, 502, "SIEVE_ACTIVATION_FAILED")
			return
		}
		if !recordSieveAudit(c, store.New(h.Pool), principal.TenantID, principal.MailboxID, "sieve.activated") {
			return
		}
		state = "active"
	}
	c.JSON(200, gin.H{"mailbox": principal.Address, "rules": rules, "csrf": h.csrf(token), "state": state, "expires_at": principal.ExpiresAt, "version": sieve.Digest(script)})
}

// Logout revokes the scoped rule editor credential.
func (h *SieveBridgeHandler) Logout(c *gin.Context) {
	var auditTenant, auditMailbox int64
	defer func() { h.failedAudit(c, auditTenant, auditMailbox) }()
	if !h.enabled(c) {
		return
	}
	token, err := c.Cookie(sieveCookie)
	if err != nil || !hmac.Equal([]byte(h.csrf(token)), []byte(c.GetHeader("X-Urmail-Sieve-CSRF"))) {
		authError(c, 403, "CSRF_INVALID")
		return
	}
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	principal, lookup := q.PlatformGetSieveBridge(c, mailboxprincipal.HashToken(token))
	if lookup != nil {
		mailboxAuthorizationError(c, lookup)
		return
	}
	auditTenant, auditMailbox = principal.TenantID, principal.MailboxID
	if q.PlatformRevokeSieveBridge(c, mailboxprincipal.HashToken(token)) != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if !recordSieveAudit(c, q, principal.TenantID, principal.MailboxID, "sieve.bridge.revoked") {
		return
	}
	if tx.Commit(c) != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(sieveCookie, "", -1, sieveCookiePath, "", true, true)
	c.Status(204)
}

func (h *SieveBridgeHandler) failedAudit(c *gin.Context, tid, mid int64) {
	if h.Pool == nil || c.Writer.Status() < 400 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	detail, _ := json.Marshal(gin.H{"actor_type": "mailbox", "mailbox_id": mid, "status": c.Writer.Status(), "outcome": "rejected"})
	if _, err := store.New(h.Pool).CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tid, Valid: tid > 0}, Action: "sieve.bridge.rejected", TargetType: pgtype.Text{String: "mailbox", Valid: true}, Detail: detail}); err != nil {
		middleware.RecordAuditFailure(false)
		zap.L().Error("write sieve rejection audit", zap.Error(err))
	}
}
