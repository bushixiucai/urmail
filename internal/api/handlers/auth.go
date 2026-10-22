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
	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

// dummyPasswordHash is verified against whenever a login names an unknown
// user, so that both failure paths perform the same expensive hash
// computation and response timing cannot enumerate accounts.
var dummyPasswordHash = func() string {
	hash, err := mail.HashPassword("urmail-dummy-password-for-timing")
	if err != nil {
		panic("password hash: " + err.Error())
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

type AuthStore interface {
	GetUserByEmail(ctx context.Context, email string) (store.GetUserByEmailRow, error)
	GetUserByID(ctx context.Context, id int64) (store.GetUserByIDRow, error)
	GetUserTenants(ctx context.Context, userID int64) ([]store.GetUserTenantsRow, error)
	CheckPlatformAdmin(ctx context.Context, userID int64) (bool, error)
	CreateSession(ctx context.Context, arg store.CreateSessionParams) (store.Sessions, error)
	DeleteSession(ctx context.Context, id string) error
	UpdateSessionTenant(ctx context.Context, arg store.UpdateSessionTenantParams) error
}

type AuthHandler struct {
	Store AuthStore
}

func NewAuthHandler(s AuthStore) *AuthHandler {
	return &AuthHandler{Store: s}
}

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

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
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "INVALID_PARAMS", "message": "common.invalid_params"},
		})
		return
	}

	limiterKey := c.ClientIP() + "|" + req.Email
	if !loginAttempts.allow(limiterKey) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": gin.H{"code": "AUTH_RATE_LIMITED", "message": "auth.rate_limited"},
		})
		return
	}

	user, err := h.Store.GetUserByEmail(c, req.Email)
	if err != nil {
		// Unknown user: burn the same hash cost as the verify path so the
		// response time does not reveal whether the account exists.
		_ = mail.VerifyPassword(req.Password, dummyPasswordHash)
		loginAttempts.fail(limiterKey)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "INVALID_CREDENTIALS", "message": "auth.invalid_credentials"},
		})
		return
	}
	if !mail.VerifyPassword(req.Password, user.PasswordHash) {
		loginAttempts.fail(limiterKey)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "INVALID_CREDENTIALS", "message": "auth.invalid_credentials"},
		})
		return
	}
	loginAttempts.reset(limiterKey)

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

	tenants, _ := h.Store.GetUserTenants(c, user.ID)
	isAdmin, _ := h.Store.CheckPlatformAdmin(c, user.ID)

	var defaultTenantID pgtype.Int8
	if len(tenants) > 0 {
		defaultTenantID = pgtype.Int8{Int64: tenants[0].ID, Valid: true}
	}

	session, err := h.Store.CreateSession(c, store.CreateSessionParams{
		ID:        middleware.HashSessionToken(token),
		UserID:    user.ID,
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

	resp := LoginResponse{
		Token:     token,
		CSRFToken: session.CsrfToken,
		Tenants:   tenants,
		IsAdmin:   isAdmin,
	}
	resp.User.ID = user.ID
	resp.User.Email = user.Email
	if user.DisplayName.Valid {
		resp.User.DisplayName = user.DisplayName.String
	}
	resp.User.Locale = user.Locale

	c.JSON(http.StatusOK, resp)
}

func (h *AuthHandler) Logout(c *gin.Context) {
	val, exists := c.Get(middleware.ContextSession)
	if exists {
		if session, ok := val.(store.Sessions); ok {
			_ = h.Store.DeleteSession(c, session.ID)
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

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

	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":           user.ID,
			"email":        user.Email,
			"display_name": user.DisplayName.String,
			"locale":       user.Locale,
		},
		"tenants":  tenants,
		"is_admin": isAdmin,
	})
}

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

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}