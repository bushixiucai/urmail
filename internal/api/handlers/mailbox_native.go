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
	Enabled   bool
	Auth      MailboxNativeAuth
	Sessions  MailboxNativeSessions
	Vault     webmail.CredentialVault
	Dialer    webmail.Dialer
	Submitter webmail.Submitter
	// Pool is required for self-service password change (platform transaction).
	Pool *db.Pool
}

// MailboxNativeAuth looks up active mailbox credentials for login.
type MailboxNativeAuth interface {
	Lookup(ctx context.Context, address string) (tenantID, mailboxID int64, canonicalAddress, passwordHash string, err error)
}

// MailboxNativeSessions persists short-lived mailbox bearer tokens.
type MailboxNativeSessions interface {
	Create(ctx context.Context, tokenHash, address string) (webmail.Session, error)
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

// Create inserts a mailbox_sessions row (same principal table as Roundcube bridge).
func (s StoreMailboxNative) Create(ctx context.Context, tokenHash, address string) (webmail.Session, error) {
	if s.Q == nil {
		return webmail.Session{}, pgx.ErrNoRows
	}
	row, err := s.Q.PlatformCreateMailboxSession(ctx, store.PlatformCreateMailboxSessionParams{
		TokenHash: tokenHash,
		Address:   address,
	})
	if err != nil {
		return webmail.Session{}, err
	}
	return webmail.Session{
		TokenHash: row.TokenHash,
		TenantID:  row.TenantID,
		MailboxID: row.MailboxID,
		Address:   address,
		ExpiresAt: timestamptzTime(row.ExpiresAt),
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
		TokenHash: row.TokenHash,
		TenantID:  row.TenantID,
		MailboxID: row.MailboxID,
		Address:   row.Address,
		ExpiresAt: timestamptzTime(row.ExpiresAt),
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
}

// Status reports whether the native webmail API is enabled.
func (h *MailboxNativeHandler) Status(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"native": h != nil && h.Enabled})
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
	if !loginAttempts.allow(limiterKey) || !loginAttempts.allowN(ipKey, loginMaxIPFailures) {
		authError(c, http.StatusTooManyRequests, "RATE_LIMITED")
		return
	}

	tenantID, mailboxID, canonical, hash, err := h.Auth.Lookup(c.Request.Context(), address)
	if err != nil {
		_ = domainMail.VerifyPassword(password, dummyPasswordHash)
		loginAttempts.fail(limiterKey)
		loginAttempts.fail(ipKey)
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	if !domainMail.VerifyPassword(password, hash) {
		loginAttempts.fail(limiterKey)
		loginAttempts.fail(ipKey)
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	loginAttempts.reset(limiterKey)
	loginAttempts.reset(ipKey)

	token, tokenHash, err := mailboxprincipal.NewToken()
	if err != nil {
		authError(c, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	session, err := h.Sessions.Create(c.Request.Context(), tokenHash, canonical)
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
		h.Vault.Put(tokenHash, password)
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
		_ = h.Sessions.Revoke(c.Request.Context(), hash)
	}
	c.JSON(http.StatusOK, gin.H{"revoked": true})
}
