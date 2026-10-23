package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/store"
)

const (
	// ContextUserID carries the authenticated actor identifier.
	ContextUserID = "urmail.user_id"
	// ContextTenantID carries the authenticated session tenant identifier.
	ContextTenantID = "urmail.tenant_id"
	// ContextSession carries the authenticated panel session.
	ContextSession = "urmail.session"
)

// SessionStore provides session lookup by token ID.
type SessionStore interface {
	GetSessionByID(ctx context.Context, id string) (store.Sessions, error)
}

// HashSessionToken derives the database identifier for a bearer token.
// Only the SHA-256 hash is persisted, so a database leak does not expose
// usable bearer tokens.
func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RequireAuth validates the Bearer token in Authorization header.
func RequireAuth(store SessionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "UNAUTHORIZED", "message": "auth.unauthorized"},
			})
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "INVALID_TOKEN", "message": "auth.invalid_token"},
			})
			return
		}

		token := strings.TrimSpace(parts[1])
		if store == nil || (reflect.ValueOf(store).Kind() == reflect.Pointer && reflect.ValueOf(store).IsNil()) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "SESSION_EXPIRED", "message": "auth.session_expired"},
			})
			return
		}
		session, err := store.GetSessionByID(c.Request.Context(), HashSessionToken(token))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "SESSION_EXPIRED", "message": "auth.session_expired"},
			})
			return
		}

		// Inject user and tenant context
		c.Set(ContextSession, session)
		c.Set(ContextUserID, session.UserID)
		if session.TenantID.Valid {
			c.Set(ContextTenantID, session.TenantID.Int64)
		}

		c.Next()
	}
}

// GetTenantID retrieves the authenticated tenant_id from gin context.
func GetTenantID(c *gin.Context) (int64, bool) {
	val, exists := c.Get(ContextTenantID)
	if !exists {
		return 0, false
	}
	id, ok := val.(int64)
	return id, ok
}

// GetUserID retrieves the authenticated user_id from gin context.
func GetUserID(c *gin.Context) (int64, bool) {
	val, exists := c.Get(ContextUserID)
	if !exists {
		return 0, false
	}
	id, ok := val.(int64)
	return id, ok
}
