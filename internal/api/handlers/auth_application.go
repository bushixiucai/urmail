package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

const applicationTokenPrefix = "urmail_app_" //nolint:gosec // Public token type marker, not a credential.
const legacyMFALifetime = 15 * time.Minute

// ApplicationCredentialStore runs only on the platform connection. Tokens are
// returned once at creation; list, authentication and audit queries omit them.
type ApplicationCredentialStore interface {
	CreateApplicationCredential(context.Context, store.CreateApplicationCredentialParams) (store.CreateApplicationCredentialRow, error)
	ListApplicationCredentials(context.Context, int64) ([]store.ListApplicationCredentialsRow, error)
	RevokeApplicationCredential(context.Context, store.RevokeApplicationCredentialParams) (int64, error)
}

// approveFreshMFA deliberately requires a new factor even if the panel session
// was created with MFA. A stolen bearer token cannot mint persistent credentials.
func (h *AuthHandler) approveFreshMFA(c *gin.Context, code string) (store.GetUserByIDRow, bool) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "UNAUTHORIZED")
		return store.GetUserByIDRow{}, false
	}
	key := c.ClientIP() + "|mfa-management:" + strconv.FormatInt(userID, 10)
	ipKey := ipKeyFromLimiterKey(key)
	if !loginAttempts.allow(key) || !loginAttempts.allowN(ipKey, loginMaxIPFailures) {
		authError(c, http.StatusTooManyRequests, "AUTH_RATE_LIMITED")
		return store.GetUserByIDRow{}, false
	}
	user, err := h.Store.GetUserByID(c, userID)
	if err != nil {
		authError(c, http.StatusUnauthorized, "UNAUTHORIZED")
		return user, false
	}
	admin, err := h.Store.CheckPlatformAdmin(c, userID)
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return user, false
	}
	if !admin {
		authError(c, http.StatusForbidden, "FORBIDDEN")
		return user, false
	}
	if !user.MfaEnabled {
		authError(c, http.StatusForbidden, "MFA_REQUIRED")
		return user, false
	}
	secret, err := h.decryptMFA(user.MfaSecret.String)
	if err != nil {
		authError(c, http.StatusInternalServerError, "INTERNAL_ERROR")
		return user, false
	}
	verified := user.MfaSecret.Valid && validateTOTP(code, secret)
	if !verified {
		n, consumeErr := h.Store.ConsumeRecoveryCode(c, store.ConsumeRecoveryCodeParams{ID: userID, Column2: hashRecoveryCode(code)})
		verified = consumeErr == nil && n > 0
	}
	if !verified {
		loginAttempts.fail(key)
		loginAttempts.fail(ipKey)
		authError(c, http.StatusUnauthorized, "MFA_INVALID_CODE")
		return user, false
	}
	loginAttempts.reset(key)
	return user, true
}

func authError(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": "error." + code}})
}

// CreateLegacySession creates a separate short-lived session after fresh MFA.
// Revoking it does not sign out the long-lived panel session.
func (h *AuthHandler) CreateLegacySession(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req MFACodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	user, ok := h.approveFreshMFA(c, req.Code)
	if !ok {
		return
	}
	token, err := randomHexToken()
	if err != nil {
		authError(c, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	csrf, err := randomHexToken()
	if err != nil {
		authError(c, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	var tenantID pgtype.Int8
	if value, exists := c.Get(middleware.ContextSession); exists {
		if current, isSession := value.(store.Sessions); isSession {
			tenantID = current.TenantID
		}
	}
	expires := time.Now().Add(legacyMFALifetime)
	session, err := h.Store.CreateSession(c, store.CreateSessionParams{ID: middleware.HashSessionToken(token), UserID: user.ID, ExpectedHash: user.PasswordHash, ExpectedVersion: user.CredentialVersion, TenantID: tenantID, CsrfToken: csrf, UserAgent: pgtype.Text{String: c.Request.UserAgent(), Valid: true}, ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true}})
	if err != nil {
		authError(c, http.StatusInternalServerError, "SESSION_CREATE_FAILED")
		return
	}
	if err = h.Store.MarkSessionLegacyOnly(c, session.ID); err != nil {
		_ = h.Store.DeleteSession(c, session.ID)
		authError(c, http.StatusInternalServerError, "SESSION_CREATE_FAILED")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"token": token, "csrf_token": csrf, "expires_at": expires})
}

// CreateApplicationCredential issues a scoped legacy token after fresh MFA.
func (h *AuthHandler) CreateApplicationCredential(c *gin.Context) {
	var req struct {
		Name      string    `json:"name" binding:"required,max=100"`
		Scopes    []string  `json:"scopes" binding:"required,min=1,max=2"`
		ExpiresAt time.Time `json:"expires_at" binding:"required"`
		Code      string    `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || !req.ExpiresAt.After(time.Now()) || req.ExpiresAt.After(time.Now().Add(365*24*time.Hour)) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	seen := map[string]bool{}
	for _, scope := range req.Scopes {
		if (scope != "legacy:read" && scope != "legacy:write") || seen[scope] {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
			return
		}
		seen[scope] = true
	}
	if h.Credentials == nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	user, ok := h.approveFreshMFA(c, req.Code)
	if !ok {
		return
	}
	random, err := randomHexToken()
	if err != nil {
		authError(c, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	token := applicationTokenPrefix + random
	credential, err := h.Credentials.CreateApplicationCredential(c, store.CreateApplicationCredentialParams{UserID: user.ID, ExpectedVersion: user.CredentialVersion, Name: req.Name, TokenHash: middleware.HashSessionToken(token), Scopes: req.Scopes, ExpiresAt: pgtype.Timestamptz{Time: req.ExpiresAt, Valid: true}})
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, gin.H{"credential": credential, "token": token})
}

// ListApplicationCredentials lists the current user's verifier-free metadata.
func (h *AuthHandler) ListApplicationCredentials(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}
	if h.Credentials == nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	credentials, err := h.Credentials.ListApplicationCredentials(c, userID)
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"credentials": credentials})
}

// RevokeApplicationCredential revokes the current user's application token.
func (h *AuthHandler) RevokeApplicationCredential(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if h.Credentials == nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if _, err = h.Credentials.RevokeApplicationCredential(c, store.RevokeApplicationCredentialParams{ID: id, UserID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			authError(c, http.StatusNotFound, "NOT_FOUND")
		} else {
			authError(c, http.StatusInternalServerError, "DB_ERROR")
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
