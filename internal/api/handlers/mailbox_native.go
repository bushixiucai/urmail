package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/eas"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func timestamptzTime(v pgtype.Timestamptz) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return v.Time
}

// MailboxNativeHandler serves the native webmail API (IMAP proxy).
// It never consumes a panel session and does not require Roundcube HMAC.
type MailboxNativeHandler struct {
	// MailHost is operator-configured, never inferred from the request Host header.
	AILocalEndpoints []string
	MailHost         string
	Enabled          bool
	Auth             MailboxNativeAuth
	Sessions         MailboxNativeSessions
	Vault            webmail.CredentialVault
	Dialer           webmail.Dialer
	Submitter        webmail.Submitter
	// MaySend reports whether this mailbox may submit mail. Nil preserves older routes.
	MaySend func(context.Context, int64, int64) (bool, error)
	// Secrets seals SMTP passwords into mailbox_scheduled_sends.smtp_secret.
	Secrets SecretsBox
	// Pool is required for self-service password change (platform transaction).
	Pool *db.Pool
	// SelfSettings serves per-mailbox signature, preferences, quota and
	// authorized sending identities; nil keeps those endpoints disabled.
	SelfSettings MailboxSelfSettingsStore
	// Sieve activates persisted mailbox rule scripts; nil keeps rules read-only.
	Sieve SieveActivator
	// Rules reads and writes the mailbox's own managed Sieve script.
	Rules MailboxRulesStore
	// EAS serves the ActiveSync transport; nil keeps the not-implemented stub.
	EAS *eas.Server
}

// MailboxNativeAuth looks up active mailbox credentials for login.
type MailboxNativeAuth interface {
	Lookup(ctx context.Context, address string) (tenantID, mailboxID int64, canonicalAddress, passwordHash string, err error)
}

// MailboxNativeSessions persists short-lived mailbox bearer tokens.
type MailboxNativeSessions interface {
	// Create inserts a session; remember extends its TTL from 30 minutes to
	// 30 days. Password-version binding and every other check are unchanged.
	Create(ctx context.Context, tokenHash, address, expectedHash string, remember bool) (webmail.Session, error)
	Get(ctx context.Context, tokenHash string) (webmail.Session, error)
	Revoke(ctx context.Context, tokenHash string) error
}

// StoreMailboxNative adapts sqlc platform queries for native webmail.
type StoreMailboxNative struct {
	Q *store.Queries
}

// Lookup resolves an active mailbox by address.
func (s StoreMailboxNative) Lookup(ctx context.Context, address string) (int64, int64, string, string, error) {
	if s.Q == nil {
		return 0, 0, "", "", pgx.ErrNoRows
	}
	row, err := s.Q.PlatformMailboxAuthLookup(ctx, address)
	if err != nil {
		return 0, 0, "", "", err
	}
	return row.TenantID, row.MailboxID, row.Address, row.PasswordHash, nil
}

// Session TTLs passed to the parameterized interval column: 30 minutes by
// default, 30 days when the login asked to remember the session. Both are
// caller constants, never raw user input. The session stays bound to
// password_version in either case.
func mailboxSessionTTL(remember bool) pgtype.Interval {
	if remember {
		return pgtype.Interval{Days: 30, Valid: true}
	}
	return pgtype.Interval{Microseconds: 30 * 60 * 1_000_000, Valid: true}
}

// Create inserts a mailbox_sessions row (same principal table as Roundcube bridge).
func (s StoreMailboxNative) Create(ctx context.Context, tokenHash, address, expectedHash string, remember bool) (webmail.Session, error) {
	if s.Q == nil {
		return webmail.Session{}, pgx.ErrNoRows
	}
	row, err := s.Q.PlatformCreateVerifiedMailboxSession(ctx, store.PlatformCreateVerifiedMailboxSessionParams{
		TokenHash:    tokenHash,
		Ttl:          mailboxSessionTTL(remember),
		ExpectedHash: expectedHash,
		OriginHost:   webmail.OriginHost(ctx),
		Address:      address,
	})
	if err != nil {
		return webmail.Session{}, err
	}
	return webmail.Session{
		OriginHost:      row.OriginHost,
		TokenHash:       row.TokenHash,
		TenantID:        row.TenantID,
		MailboxID:       row.MailboxID,
		PasswordVersion: row.PasswordVersion,
		Address:         address,
		ExpiresAt:       timestamptzTime(row.ExpiresAt),
	}, nil
}

// Get loads a still-valid mailbox session.
func (s StoreMailboxNative) Get(ctx context.Context, tokenHash string) (webmail.Session, error) {
	if s.Q == nil {
		return webmail.Session{}, pgx.ErrNoRows
	}
	row, err := s.Q.PlatformGetMailboxSession(ctx, tokenHash)
	if err != nil {
		return webmail.Session{}, err
	}
	return webmail.Session{
		OriginHost:      row.OriginHost,
		TokenHash:       row.TokenHash,
		TenantID:        row.TenantID,
		MailboxID:       row.MailboxID,
		PasswordVersion: row.PasswordVersion,
		Address:         row.Address,
		ExpiresAt:       timestamptzTime(row.ExpiresAt),
	}, nil
}

// Revoke marks the session revoked.
func (s StoreMailboxNative) Revoke(ctx context.Context, tokenHash string) error {
	if s.Q == nil {
		return nil
	}
	return s.Q.PlatformRevokeMailboxSession(ctx, tokenHash)
}

// GetMailboxSession satisfies middleware.MailboxSessionLookup.
func (s StoreMailboxNative) GetMailboxSession(ctx context.Context, tokenHash string) (webmail.Session, error) {
	return s.Get(ctx, tokenHash)
}

type mailboxNativeLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// Remember extends the session TTL from 30 minutes to 30 days. The session
	// stays bound to password_version, so a password change still revokes it.
	Remember bool `json:"remember"`
}

// Status reports whether the native webmail API is enabled.
func (h *MailboxNativeHandler) Status(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	host := ""
	if h != nil {
		host = h.MailHost
	}
	c.JSON(http.StatusOK, gin.H{"native": h != nil && h.Enabled, "mail_host": host})
}

func (h *MailboxNativeHandler) requireEnabled(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	if h == nil || !h.Enabled || h.Auth == nil || h.Sessions == nil {
		authError(c, http.StatusServiceUnavailable, "WEBMAIL_DISABLED")
		return false
	}
	return true
}

// CreateSession authenticates a mailbox email+password and returns a bearer token.
// Panel accounts are never accepted here.
func (h *MailboxNativeHandler) CreateSession(c *gin.Context) {
	if !h.requireEnabled(c) {
		return
	}
	var req mailboxNativeLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	address := strings.TrimSpace(strings.ToLower(req.Email))
	password := req.Password
	if address == "" || len(address) > 320 || password == "" || len(password) > 4096 || !utf8.ValidString(password) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}

	ipKey := "mbox-ip|" + c.ClientIP()
	limiterKey := "mbox|" + c.ClientIP() + "|" + address
	reservation, err := loginAttempts.BeginAttempt(limiterKey, ipKey, DefaultLoginBudgets)
	if err != nil {
		authError(c, http.StatusTooManyRequests, "RATE_LIMITED")
		return
	}
	defer reservation.Discard()

	tenantID, mailboxID, canonical, hash, err := h.Auth.Lookup(c.Request.Context(), address)
	if err != nil {
		if _, verifyErr := domainMail.VerifyPasswordContext(c.Request.Context(), password, dummyMailboxPasswordHash); verifyErr != nil {
			authError(c, 429, "RATE_LIMITED")
			return
		}
		reservation.RecordFailure()
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	if brand, ok := c.Get(middleware.ContextBrandID); ok {
		bound, ok := h.Sessions.(interface {
			BrandTenantMatches(context.Context, int64, int64) bool
		})
		if !ok || !bound.BrandTenantMatches(c, tenantID, brand.(int64)) {
			authError(c, 403, "BRAND_HOST_TENANT_MISMATCH")
			return
		}
	}
	matches, verifyErr := domainMail.VerifyPasswordContext(c.Request.Context(), password, hash)
	if verifyErr != nil {
		authError(c, 429, "RATE_LIMITED")
		return
	}
	if !matches {
		reservation.RecordFailure()
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	reservation.CompleteSuccess()

	token, tokenHash, err := mailboxprincipal.NewToken()
	if err != nil {
		authError(c, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	session, err := h.Sessions.Create(c.Request.Context(), tokenHash, canonical, hash, req.Remember)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
			return
		}
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if session.TenantID == 0 {
		session.TenantID = tenantID
	}
	if session.MailboxID == 0 {
		session.MailboxID = mailboxID
	}
	if session.Address == "" {
		session.Address = canonical
	}
	if h.Vault != nil {
		if bounded, ok := h.Vault.(webmail.SessionVault); ok {
			if bounded.PutSession(session, password) != nil {
				_ = h.Sessions.Revoke(c.Request.Context(), tokenHash)
				authError(c, 429, "RATE_LIMITED")
				return
			}
		} else {
			h.Vault.Put(tokenHash, password)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"token":      token,
		"expires_at": session.ExpiresAt.UTC().Format(time.RFC3339),
		"address":    session.Address,
	})
}

// ListFolders returns IMAP folders for the authenticated mailbox principal.
func (h *MailboxNativeHandler) ListFolders(c *gin.Context) {
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	folders, err := client.ListFolders(c.Request.Context())
	if err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	if folders == nil {
		folders = []webmail.Folder{}
	}
	c.JSON(http.StatusOK, gin.H{"folders": folders})
}

// dialIMAP opens an authenticated IMAP client using the vaulted session password.
// On failure it has already written the HTTP error response.
func (h *MailboxNativeHandler) dialIMAP(c *gin.Context) (webmail.Client, webmail.Session, bool) {
	if !h.requireEnabled(c) {
		return nil, webmail.Session{}, false
	}
	if h.Dialer == nil || h.Vault == nil {
		authError(c, http.StatusServiceUnavailable, "WEBMAIL_DISABLED")
		return nil, webmail.Session{}, false
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return nil, webmail.Session{}, false
	}
	password, ok := h.vaultPassword(c)
	if !ok {
		return nil, webmail.Session{}, false
	}
	client, err := h.Dialer.Dial(c.Request.Context(), session.Address, password)
	if err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return nil, webmail.Session{}, false
	}
	return client, session, true
}

func (h *MailboxNativeHandler) vaultPassword(c *gin.Context) (string, bool) {
	tokenHash, _ := c.Get(middleware.ContextMailboxTokenHash)
	hash, _ := tokenHash.(string)
	password, ok := h.Vault.Get(hash)
	if !ok || password == "" {
		// Process restarted or vault miss: bearer is still valid in PG but IMAP
		// cannot be dialed without the secret — force re-login.
		authError(c, http.StatusUnauthorized, "MAILBOX_REAUTH_REQUIRED")
		return "", false
	}
	return password, true
}

func (h *MailboxNativeHandler) folderParam(c *gin.Context) (string, bool) {
	name, err := webmail.DecodeFolderName(c.Param("name"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return "", false
	}
	return name, true
}

func bearerToken(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// DeleteSession revokes the bearer token and clears the in-memory IMAP secret.
func (h *MailboxNativeHandler) DeleteSession(c *gin.Context) {
	if !h.requireEnabled(c) {
		return
	}
	tokenHash, _ := c.Get(middleware.ContextMailboxTokenHash)
	hash, _ := tokenHash.(string)
	if h.Vault != nil && hash != "" {
		h.Vault.Delete(hash)
	}
	if hash != "" && h.Sessions != nil {
		if err := h.Sessions.Revoke(c.Request.Context(), hash); err != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"revoked": true})
}

// BrandTenantMatches preserves the current brand boundary for native sessions.
func (s StoreMailboxNative) BrandTenantMatches(ctx context.Context, tenant, brand int64) bool {
	if s.Q == nil {
		return false
	}
	ok, err := s.Q.PlatformTenantBrandMatches(ctx, store.PlatformTenantBrandMatchesParams{ID: tenant, BrandID: brand})
	return err == nil && ok
}
