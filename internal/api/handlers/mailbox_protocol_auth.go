package handlers

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// ProtocolAuth accepts standard client credentials over TLS (or the local
// TLS-terminating proxy). A Basic request gets a request-scoped, verified
// mailbox session; neither panel credentials nor an arbitrary mailbox ID can
// establish a protocol principal. Bearer clients keep the existing contract.
func (h *MailboxNativeHandler) ProtocolAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
		if err != nil {
			host = c.Request.RemoteAddr
		}
		if c.Request.TLS == nil && !net.ParseIP(host).IsLoopback() {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(strings.ToLower(c.GetHeader("Authorization")), "basic ") {
			c.Header("WWW-Authenticate", `Basic realm="urMail", charset="UTF-8"`)
			middleware.RequireMailboxAuth(protocolSessionLookup{h}, h.Vault)(c)
			return
		}
		address, password, ok := c.Request.BasicAuth()
		address = strings.ToLower(strings.TrimSpace(address))
		if !ok || h.Auth == nil || h.Sessions == nil || h.Vault == nil || address == "" || len(address) > 320 || password == "" || len(password) > 4096 || !utf8.ValidString(password) {
			protocolUnauthorized(c)
			return
		}
		ipKey := "mbox-ip|" + c.ClientIP()
		key := "mbox|" + c.ClientIP() + "|" + address
		if !loginAttempts.allow(key) || !loginAttempts.allowN(ipKey, loginMaxIPFailures) {
			c.AbortWithStatus(http.StatusTooManyRequests)
			return
		}
		_, _, canonical, hash, lookupErr := h.Auth.Lookup(c.Request.Context(), address)
		if lookupErr != nil {
			hash = dummyMailboxPasswordHash
		}
		matches, verifyErr := domainMail.VerifyPasswordContext(c.Request.Context(), password, hash)
		if verifyErr != nil {
			c.AbortWithStatus(http.StatusTooManyRequests)
			return
		}
		if lookupErr != nil || !matches {
			loginAttempts.fail(key)
			loginAttempts.fail(ipKey)
			protocolUnauthorized(c)
			return
		}
		token, tokenHash, err := mailboxprincipal.NewToken()
		if err != nil {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		// The verified-session insert compares the live hash and account state,
		// closing the password-change race after the initial lookup.
		session, err := h.Sessions.Create(c.Request.Context(), tokenHash, canonical, hash)
		if err != nil || session.TenantID <= 0 || session.MailboxID <= 0 || session.Address == "" {
			protocolUnauthorized(c)
			return
		}
		defer func() {
			h.Vault.Delete(tokenHash)
			ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Second)
			defer cancel()
			_ = h.Sessions.Revoke(ctx, tokenHash)
		}()
		if bounded, ok := h.Vault.(webmail.SessionVault); ok {
			if bounded.PutSession(session, password) != nil {
				c.AbortWithStatus(http.StatusTooManyRequests)
				return
			}
		} else {
			h.Vault.Put(tokenHash, password)
		}
		loginAttempts.reset(key)
		loginAttempts.reset(ipKey)
		// Only the in-process request sees the token. It is never returned to
		// Basic clients or retained in a URL, cookie or configuration file.
		c.Request.Header.Set("Authorization", "Bearer "+token)
		middleware.RequireMailboxAuth(protocolSessionLookup{h}, h.Vault)(c)
	}
}

type protocolSessionLookup struct{ handler *MailboxNativeHandler }

func (s protocolSessionLookup) GetMailboxSession(ctx context.Context, hash string) (webmail.Session, error) {
	if s.handler == nil || s.handler.Sessions == nil {
		return webmail.Session{}, errors.New("mailbox protocol sessions unavailable")
	}
	return s.handler.Sessions.Get(ctx, hash)
}

func protocolUnauthorized(c *gin.Context) {
	c.Header("WWW-Authenticate", `Basic realm="urMail", charset="UTF-8"`)
	c.AbortWithStatus(http.StatusUnauthorized)
}
