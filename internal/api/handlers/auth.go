package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/domain/passwordwork"
	"github.com/bushixiucai/urmail/internal/store"
)

// dummyPasswordHash is verified against whenever a login names an unknown
// user, so that both failure paths perform the same expensive hash
// computation and response timing cannot enumerate accounts.
var dummyPasswordHash = func() string {
	hash, err := panelpassword.Hash("urmail-dummy-password-for-timing")
	if err != nil {
		panic("password hash: " + err.Error())
	}
	return hash
}()

var dummyMailboxPasswordHash = func() string {
	hash, err := domainMail.HashPassword("urmail-dummy-password-for-timing")
	if err != nil {
		panic(err)
	}
	return hash
}()

func randomHexToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// AuthStore is the platform-scoped authentication persistence boundary.
type AuthStore interface {
	PlatformRotatePanelPassword(context.Context, store.PlatformRotatePanelPasswordParams) (int64, error)
	PlatformChangePanelMFA(context.Context, store.PlatformChangePanelMFAParams) (int64, error)
	PlatformRehashPanelPassword(context.Context, store.PlatformRehashPanelPasswordParams) (int64, error)
	GetUserByEmail(ctx context.Context, email string) (store.GetUserByEmailRow, error)
	GetUserByID(ctx context.Context, id int64) (store.GetUserByIDRow, error)
	GetUserTenants(ctx context.Context, userID int64) ([]store.GetUserTenantsRow, error)
	CheckPlatformAdmin(ctx context.Context, userID int64) (bool, error)
	CreateSession(ctx context.Context, arg store.CreateSessionParams) (store.Sessions, error)
	DeleteSession(ctx context.Context, id string) error
	MarkSessionMFAVerified(ctx context.Context, id string) error
	MarkSessionLegacyOnly(ctx context.Context, id string) error
	UpdateSessionTenant(ctx context.Context, arg store.UpdateSessionTenantParams) error
	SetMFASecret(ctx context.Context, arg store.SetMFASecretParams) error
	EnableUserMFA(ctx context.Context, arg store.EnableUserMFAParams) error
	DisableUserMFA(ctx context.Context, id int64) error
	ConsumeRecoveryCode(ctx context.Context, arg store.ConsumeRecoveryCodeParams) (int64, error)
	UpdateUserPassword(ctx context.Context, arg store.UpdateUserPasswordParams) error
	DeleteOtherUserSessions(ctx context.Context, arg store.DeleteOtherUserSessionsParams) error
	ListSessionsForUser(ctx context.Context, userID int64) ([]store.ListSessionsForUserRow, error)
	DeleteUserSession(ctx context.Context, arg store.DeleteUserSessionParams) (int64, error)
}

// AuthHandler serves panel authentication and session management.
type AuthHandler struct {
	Store       AuthStore
	Secrets     SecretsBox
	Credentials ApplicationCredentialStore
}

// SecretsBox is the subset of secrets.Box used by the auth handler.
type SecretsBox interface {
	EncryptString(plain string) (string, error)
	DecryptString(value string) (string, error)
}

// NewAuthHandler wires authentication and optional application credentials.
func NewAuthHandler(s AuthStore) *AuthHandler {
	h := &AuthHandler{Store: s}
	if credentials, ok := s.(ApplicationCredentialStore); ok {
		h.Credentials = credentials
	}
	return h
}

func (h *AuthHandler) decryptMFA(value string) (string, error) {
	if h.Secrets == nil {
		return value, nil
	}
	return h.Secrets.DecryptString(value)
}

// LoginRequest supplies the first authentication factor.
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse returns a session or a pending second-factor challenge.
type LoginResponse struct {
	Token     string `json:"token"`
	CSRFToken string `json:"csrf_token"`
	User      struct {
		ID          int64  `json:"id"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Locale      string `json:"locale"`
	} `json:"user"`
	Tenants []store.GetUserTenantsRow `json:"tenants"`
	IsAdmin bool                      `json:"is_admin"`

	// CurrentTenantID is the tenant the new session is bound to. The browser
	// must use this value instead of assuming tenants[0].
	CurrentTenantID *int64 `json:"current_tenant_id,omitempty"`

	// Set on the first login leg when the user has MFA enabled: no session
	// is created until the second factor is verified via /auth/mfa/verify.
	MfaRequired bool   `json:"mfa_required,omitempty"`
	MfaToken    string `json:"mfa_token,omitempty"`
}

// Login verifies the password and requires a second factor when enabled.
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil || !passwordwork.ValidVerification(req.Password) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "INVALID_PARAMS", "message": "common.invalid_params"},
		})
		return
	}

	limiterKey := c.ClientIP() + "|" + req.Email
	ipKey := ipKeyFromLimiterKey(limiterKey)
	if !loginAttempts.allow(limiterKey) || !loginAttempts.allowN(ipKey, loginMaxIPFailures) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": gin.H{"code": "AUTH_RATE_LIMITED", "message": "auth.rate_limited"},
		})
		return
	}

	user, err := h.Store.GetUserByEmail(c, req.Email)
	if err != nil {
		// Unknown user: burn the same hash cost as the verify path so the
		// response time does not reveal whether the account exists.
		if _, available := panelPasswordMatches(c, req.Password, dummyPasswordHash); !available {
			return
		}
		loginAttempts.fail(limiterKey)
		loginAttempts.fail(ipKey)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "INVALID_CREDENTIALS", "message": "auth.invalid_credentials"},
		})
		return
	}
	matches, available := panelPasswordMatches(c, req.Password, user.PasswordHash)
	if !available {
		return
	}
	if !matches {
		if panelpassword.NeedsUpgrade(user.PasswordHash) {
			if _, available := panelPasswordMatches(c, req.Password, dummyPasswordHash); !available {
				return
			}
		}
		loginAttempts.fail(limiterKey)
		loginAttempts.fail(ipKey)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "INVALID_CREDENTIALS", "message": "auth.invalid_credentials"},
		})
		return
	}
	if panelpassword.NeedsUpgrade(user.PasswordHash) {
		upgraded, hashErr := panelpassword.Rehash(c.Request.Context(), req.Password)
		if hashErr != nil {
			authError(c, 429, "AUTH_RATE_LIMITED")
			return
		}
		n, upgradeErr := h.Store.PlatformRehashPanelPassword(c, store.PlatformRehashPanelPasswordParams{UserID: user.ID, ExpectedHash: user.PasswordHash, PasswordHash: upgraded})
		if upgradeErr != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
		if n != 1 {
			fresh, readErr := h.Store.GetUserByEmail(c, req.Email)
			if readErr != nil || fresh.CredentialVersion != user.CredentialVersion {
				authError(c, 401, "INVALID_CREDENTIALS")
				return
			}
			matches, available := panelPasswordMatches(c, req.Password, fresh.PasswordHash)
			if !available {
				return
			}
			if !matches {
				authError(c, 401, "INVALID_CREDENTIALS")
				return
			}
			user = fresh
		} else {
			user.PasswordHash = upgraded
		}
	}
	if user.MfaEnabled {
		// The limiter is only reset once BOTH factors pass (MFAVerify);
		// second-leg failures keep counting on the same key.
		mfaToken, err := pendingMFATokens.issueForRequest(c, user.ID, limiterKey, user.CredentialVersion)
		if err != nil {
			authError(c, 429, "AUTH_RATE_LIMITED")
			return
		}
		c.JSON(http.StatusOK, gin.H{"mfa_required": true, "mfa_token": mfaToken})
		return
	}

	loginAttempts.reset(limiterKey)
	loginAttempts.reset(ipKey)
	h.establishSession(c, user.ID, user.Email, user.DisplayName, user.Locale, false, user.PasswordHash, user.CredentialVersion)
}

// establishSession creates the session row and writes the full login
// response. It is shared by the single-factor login path and the second
// MFA leg.
func (h *AuthHandler) establishSession(c *gin.Context, userID int64, email string, displayName pgtype.Text, locale string, mfaVerified bool, expectedHash string, expectedVersion int64) {
	c.Header("Cache-Control", "no-store")
	token, err := randomHexToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{"code": "INTERNAL_ERROR"},
		})
		return
	}

	csrfToken, err := randomHexToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{"code": "INTERNAL_ERROR"},
		})
		return
	}

	tenants, _ := h.Store.GetUserTenants(c, userID)
	isAdmin, _ := h.Store.CheckPlatformAdmin(c, userID)
	if brand, ok := c.Get(middleware.ContextBrandID); ok {
		filtered := make([]store.GetUserTenantsRow, 0, len(tenants))
		for _, tenant := range tenants {
			if tenant.BrandID == brand.(int64) {
				filtered = append(filtered, tenant)
			}
		}
		tenants = filtered
		isAdmin = false
		if len(tenants) == 0 {
			authError(c, 403, "BRAND_HOST_TENANT_MISMATCH")
			return
		}
	}

	var defaultTenantID pgtype.Int8
	if len(tenants) > 0 {
		defaultTenantID = pgtype.Int8{Int64: tenants[0].ID, Valid: true}
	}

	session, err := h.Store.CreateSession(c, store.CreateSessionParams{
		ID:           middleware.HashSessionToken(token),
		OriginHost:   middleware.OriginHost(c.Request.Host),
		UserID:       userID,
		ExpectedHash: expectedHash, ExpectedVersion: expectedVersion,
		TenantID:  defaultTenantID,
		CsrfToken: csrfToken,
		UserAgent: pgtype.Text{String: c.Request.UserAgent(), Valid: true},
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(7 * 24 * time.Hour), Valid: true},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{"code": "SESSION_CREATE_FAILED", "message": "auth.session_failed"},
		})
		return
	}

	if mfaVerified {
		if err := h.Store.MarkSessionMFAVerified(c, session.ID); err != nil {
			if err := h.Store.DeleteSession(c, session.ID); err != nil {
				authError(c, 503, "DB_ERROR")
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "SESSION_CREATE_FAILED"}})
			return
		}
	}

	resp := LoginResponse{
		Token:     token,
		CSRFToken: session.CsrfToken,
		Tenants:   tenants,
		IsAdmin:   isAdmin,
	}
	resp.User.ID = userID
	resp.User.Email = email
	if displayName.Valid {
		resp.User.DisplayName = displayName.String
	}
	resp.User.Locale = locale
	if defaultTenantID.Valid {
		tenantID := defaultTenantID.Int64
		resp.CurrentTenantID = &tenantID
	}

	c.JSON(http.StatusOK, resp)
}

// Logout revokes the authenticated session.
func (h *AuthHandler) Logout(c *gin.Context) {
	val, exists := c.Get(middleware.ContextSession)
	if exists {
		if session, ok := val.(store.Sessions); ok {
			if err := h.Store.DeleteSession(c, session.ID); err != nil {
				authError(c, 503, "DB_ERROR")
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Me returns the authenticated identity and authorized tenant memberships.
func (h *AuthHandler) Me(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	user, err := h.Store.GetUserByID(c, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "USER_NOT_FOUND"}})
		return
	}

	tenants, _ := h.Store.GetUserTenants(c, userID)
	isAdmin, _ := h.Store.CheckPlatformAdmin(c, userID)
	if brand, branded := c.Get(middleware.ContextBrandID); branded {
		visible := make([]store.GetUserTenantsRow, 0, len(tenants))
		for _, tenant := range tenants {
			if tenant.BrandID == brand.(int64) {
				visible = append(visible, tenant)
			}
		}
		tenants = visible
		isAdmin = false
	}

	var currentTenantID *int64
	if val, exists := c.Get(middleware.ContextSession); exists {
		if session, ok := val.(store.Sessions); ok && session.TenantID.Valid {
			id := session.TenantID.Int64
			currentTenantID = &id
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":           user.ID,
			"email":        user.Email,
			"display_name": user.DisplayName.String,
			"locale":       user.Locale,
		},
		"tenants":           tenants,
		"is_admin":          isAdmin,
		"current_tenant_id": currentTenantID,
	})
}

// SwitchTenantRequest identifies an authorized destination tenant.
type SwitchTenantRequest struct {
	TenantID int64 `json:"tenant_id" binding:"required"`
}

// SwitchTenant moves the current session to another tenant the user is a
// member of. Membership is checked against user_tenant_roles, so a session
// can never be pointed at an unrelated tenant.
func (h *AuthHandler) SwitchTenant(c *gin.Context) {
	var req SwitchTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "INVALID_PARAMS", "message": "common.invalid_params"},
		})
		return
	}

	session, ok := c.Get(middleware.ContextSession)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	s, ok := session.(store.Sessions)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	tenants, err := h.Store.GetUserTenants(c, s.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	member := false
	for _, t := range tenants {
		if t.ID == req.TenantID {
			if brand, ok := c.Get(middleware.ContextBrandID); ok && t.BrandID != brand.(int64) {
				continue
			}
			member = true
			break
		}
	}
	if !member {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{"code": "FORBIDDEN", "message": "error.FORBIDDEN"},
		})
		return
	}

	if err := h.Store.UpdateSessionTenant(c, store.UpdateSessionTenantParams{
		ID:       s.ID,
		TenantID: pgtype.Int8{Int64: req.TenantID, Valid: true},
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "tenant_id": req.TenantID})
}

// keepSessionID identifies only the already authenticated current session.
func keepSessionID(c *gin.Context) string {
	if value, exists := c.Get(middleware.ContextSession); exists {
		if session, ok := value.(store.Sessions); ok {
			return session.ID
		}
	}
	return ""
}

func panelPasswordMatches(c *gin.Context, password, hash string) (bool, bool) {
	matches, err := panelpassword.VerifyContext(c.Request.Context(), password, hash)
	if err != nil {
		authError(c, 429, "AUTH_RATE_LIMITED")
		return false, false
	}
	return matches, true
}
