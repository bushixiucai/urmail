package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/webmail"
)

const (
	// ContextMailboxSession carries the authenticated native webmail session.
	ContextMailboxSession = "urmail.mailbox_session"
	// ContextMailboxAddress is the mailbox email for the bearer token.
	ContextMailboxAddress = "urmail.mailbox_address"
	// ContextMailboxTokenHash is the hashed bearer (for credential vault lookup).
	ContextMailboxTokenHash = "urmail.mailbox_token_hash" //nolint:gosec // Context lookup key, not a credential.
)

// MailboxSessionLookup resolves a still-valid mailbox_sessions row.
type MailboxSessionLookup interface {
	GetMailboxSession(ctx context.Context, tokenHash string) (webmail.Session, error)
}

// RequireMailboxAuth validates a Bearer mailbox token. It never accepts a
// panel session and does not require Roundcube HMAC.
func RequireMailboxAuth(lookup MailboxSessionLookup, vaults ...webmail.CredentialVault) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if lookup == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": gin.H{"code": "WEBMAIL_DISABLED", "message": "error.WEBMAIL_DISABLED"},
			})
			return
		}
		authHeader := c.GetHeader("Authorization")
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "UNAUTHORIZED", "message": "error.UNAUTHORIZED"},
			})
			return
		}
		token := strings.TrimSpace(parts[1])
		hash := mailboxprincipal.HashToken(token)
		session, err := lookup.GetMailboxSession(c.Request.Context(), hash)
		if err != nil || session.Address == "" || session.ExpiresAt.Before(time.Now()) {
			for _, vault := range vaults {
				if vault != nil {
					vault.Delete(hash)
				}
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "MAILBOX_AUTHORIZATION_DENIED", "message": "error.MAILBOX_AUTHORIZATION_DENIED"},
			})
			return
		}
		c.Set(ContextMailboxSession, session)
		c.Set(ContextMailboxAddress, session.Address)
		c.Set(ContextMailboxTokenHash, hash)
		c.Set(ContextTenantID, session.TenantID)
		c.Next()
	}
}

// GetMailboxSession returns the native webmail session from context.
func GetMailboxSession(c *gin.Context) (webmail.Session, bool) {
	val, ok := c.Get(ContextMailboxSession)
	if !ok {
		return webmail.Session{}, false
	}
	session, ok := val.(webmail.Session)
	return session, ok
}
