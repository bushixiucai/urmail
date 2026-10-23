// Package handlers provides the legacy Mail-in-a-Box compatibility surface.
//
// The old /admin API is intentionally small: it exposes the read paths used
// by the legacy UI plus mailbox/alias creation. Authentication is HTTP Basic
// with a platform administrator account; every route is tenant-scoped to the
// first tenant, matching the single-tenant assumption of the old API.
package handlers

import (
	"context"
	"crypto/subtle"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/certs"
	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

// LegacyAuthStore resolves active users and live platform roles.
type LegacyAuthStore interface {
	GetUserByEmail(ctx context.Context, email string) (store.GetUserByEmailRow, error)
	CheckPlatformAdmin(ctx context.Context, userID int64) (bool, error)
}

// LegacyStore restricts compatibility operations to the configured tenant.
type LegacyStore interface {
	ListMailboxesByTenant(ctx context.Context, tenantID int64) ([]store.Mailboxes, error)
	ListDomainsByTenant(ctx context.Context, tenantID int64) ([]store.ListDomainsByTenantRow, error)
	ListAliasesByTenant(ctx context.Context, tenantID int64) ([]store.ListAliasesByTenantRow, error)
	GetDomainByFQDN(ctx context.Context, arg store.GetDomainByFQDNParams) (store.GetDomainByFQDNRow, error)
	CreateMailbox(ctx context.Context, arg store.CreateMailboxParams) (store.Mailboxes, error)
	CreateAlias(ctx context.Context, arg store.CreateAliasParams) (store.MailboxAliases, error)
	GetMailboxByAddress(ctx context.Context, arg store.GetMailboxByAddressParams) (store.Mailboxes, error)
	UpdateMailboxPassword(ctx context.Context, arg store.UpdateMailboxPasswordParams) (store.Mailboxes, error)
	DeleteMailbox(ctx context.Context, arg store.DeleteMailboxParams) error
	DeleteAlias(ctx context.Context, arg store.DeleteAliasParams) error
}

// LegacyHandler serves the explicitly supported compatibility endpoints.
type LegacyHandler struct {
	Auth     LegacyAuthStore
	Store    LegacyStore
	TenantID int64
	Version  string
}

// NewLegacyHandler binds legacy operations to one configured tenant.
func NewLegacyHandler(auth LegacyAuthStore, s LegacyStore, tenantID int64, version string) *LegacyHandler {
	if tenantID <= 0 {
		tenantID = 1
	}
	return &LegacyHandler{Auth: auth, Store: s, TenantID: tenantID, Version: version}
}

// LegacyAccessStore uses the platform pool, but checks the creator's live
// status and administrator role on every request. Credentials never grant roles.
type LegacyAccessStore interface {
	GetLegacySession(context.Context, string) (store.GetLegacySessionRow, error)
	AuthenticateApplicationCredential(context.Context, store.AuthenticateApplicationCredentialParams) (store.AuthenticateApplicationCredentialRow, error)
	AuditLegacyCredentialUse(context.Context, store.AuditLegacyCredentialUseParams) error
}

func (h *LegacyHandler) authorize(c *gin.Context) bool {
	// Browser-cached Basic authentication must not permit cross-site form writes.
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		for _, header := range []string{"Origin", "Referer"} {
			if value := c.GetHeader(header); value != "" {
				origin, err := url.Parse(value)
				if err != nil || !strings.EqualFold(origin.Host, c.Request.Host) {
					authError(c, http.StatusForbidden, "CSRF_INVALID_ORIGIN")
					return false
				}
			}
		}
	}
	access, _ := h.Auth.(LegacyAccessStore)
	if strings.HasPrefix(strings.ToLower(c.GetHeader("Authorization")), "bearer ") {
		if access == nil {
			authError(c, http.StatusUnauthorized, "UNAUTHORIZED")
			return false
		}
		token := strings.TrimSpace(c.GetHeader("Authorization")[7:])
		session, err := access.GetLegacySession(c, middleware.HashSessionToken(token))
		if err != nil {
			authError(c, http.StatusUnauthorized, "MFA_REQUIRED")
			return false
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			csrf := c.GetHeader(middleware.CSRFHeader)
			if csrf == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(session.CsrfToken)) != 1 {
				authError(c, http.StatusForbidden, "CSRF_INVALID")
				return false
			}
		}
		c.Set(middleware.ContextUserID, session.UserID)
		return h.auditLegacyAccess(c, access, session.UserID, "session")
	}
	email, password, ok := c.Request.BasicAuth()
	if !ok {
		c.Header("WWW-Authenticate", `Basic realm="urMail"`)
		authError(c, http.StatusUnauthorized, "UNAUTHORIZED")
		return false
	}
	email = strings.ToLower(strings.TrimSpace(email))
	key := c.ClientIP() + "|" + email
	ipKey := ipKeyFromLimiterKey(key)
	if !loginAttempts.allow(key) || !loginAttempts.allowN(ipKey, loginMaxIPFailures) {
		authError(c, http.StatusTooManyRequests, "AUTH_RATE_LIMITED")
		return false
	}
	reject := func(code string) bool {
		loginAttempts.fail(key)
		loginAttempts.fail(ipKey)
		authError(c, http.StatusForbidden, code)
		return false
	}
	if strings.HasPrefix(password, applicationTokenPrefix) {
		if access == nil {
			return reject("INVALID_CREDENTIALS")
		}
		credential, err := access.AuthenticateApplicationCredential(c, store.AuthenticateApplicationCredentialParams{TokenHash: middleware.HashSessionToken(password), Email: email})
		if err != nil {
			return reject("INVALID_CREDENTIALS")
		}
		needed := "legacy:read"
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			needed = "legacy:write"
		}
		allowed := false
		for _, scope := range credential.Scopes {
			if scope == needed {
				allowed = true
			}
		}
		if !allowed {
			return reject("FORBIDDEN_SCOPE")
		}
		c.Set(middleware.ContextUserID, credential.UserID)
		if !h.auditLegacyAccess(c, access, credential.UserID, "credential:"+strconv.FormatInt(credential.ID, 10)) {
			return false
		}
		loginAttempts.reset(key)
		return true
	}
	user, err := h.Auth.GetUserByEmail(c, email)
	if err != nil {
		_ = mail.VerifyPassword(password, dummyPasswordHash)
		return reject("INVALID_CREDENTIALS")
	}
	if !mail.VerifyPassword(password, user.PasswordHash) {
		return reject("INVALID_CREDENTIALS")
	}
	if user.MfaEnabled {
		return reject("MFA_REQUIRED")
	}
	admin, err := h.Auth.CheckPlatformAdmin(c, user.ID)
	if err != nil || !admin {
		return reject("FORBIDDEN")
	}
	c.Set(middleware.ContextUserID, user.ID)
	if !h.auditLegacyAccess(c, access, user.ID, "basic") {
		return false
	}
	loginAttempts.reset(key)
	return true
}

func (h *LegacyHandler) auditLegacyAccess(c *gin.Context, access LegacyAccessStore, userID int64, target string) bool {
	// Authorization attempts are recorded before the protected operation. The
	// event describes credential use, not a claim that a later mutation committed.
	if access == nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return false
	}
	err := access.AuditLegacyCredentialUse(c, store.AuditLegacyCredentialUseParams{ActorUserID: pgtype.Int8{Int64: userID, Valid: true}, Action: "legacy.authorize " + c.Request.Method + " " + c.FullPath(), TargetID: pgtype.Text{String: target, Valid: true}})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return false
	}
	return true
}

// Status reports compatibility service status.
func (h *LegacyHandler) Status(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "version": h.Version})
}

// VersionInfo reports the installed version.
func (h *LegacyHandler) VersionInfo(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": h.Version})
}

type legacyMailUser struct {
	Email      string   `json:"email"`
	Privileges []string `json:"privileges"`
	Status     string   `json:"status"`
}

type legacyMailUsersByDomain struct {
	Domain string           `json:"domain"`
	Users  []legacyMailUser `json:"users"`
}

// MailUsers lists mailbox addresses for the configured tenant.
func (h *LegacyHandler) MailUsers(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	boxes, err := h.Store.ListMailboxesByTenant(c, h.TenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db"})
		return
	}
	domains, _ := h.Store.ListDomainsByTenant(c, h.TenantID)
	byID := make(map[int64]string, len(domains))
	for _, d := range domains {
		byID[d.ID] = d.Fqdn
	}
	grouped := map[string][]legacyMailUser{}
	order := []string{}
	for _, box := range boxes {
		domain := byID[box.DomainID]
		if _, ok := grouped[domain]; !ok {
			order = append(order, domain)
		}
		grouped[domain] = append(grouped[domain], legacyMailUser{
			Email:      box.Address,
			Privileges: []string{},
			Status:     box.Status,
		})
	}
	out := make([]legacyMailUsersByDomain, 0, len(order))
	for _, domain := range order {
		out = append(out, legacyMailUsersByDomain{Domain: domain, Users: grouped[domain]})
	}
	c.JSON(http.StatusOK, out)
}

// MailDomains lists domains for the configured tenant.
func (h *LegacyHandler) MailDomains(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	domains, err := h.Store.ListDomainsByTenant(c, h.TenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db"})
		return
	}
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		out = append(out, d.Fqdn)
	}
	c.JSON(http.StatusOK, out)
}

// DNSZones lists the configured tenant's DNS zones.
func (h *LegacyHandler) DNSZones(c *gin.Context) {
	h.MailDomains(c)
}

// MailAliases lists aliases for the configured tenant.
func (h *LegacyHandler) MailAliases(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	aliases, err := h.Store.ListAliasesByTenant(c, h.TenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db"})
		return
	}
	domains, _ := h.Store.ListDomainsByTenant(c, h.TenantID)
	byID := make(map[int64]string, len(domains))
	for _, d := range domains {
		byID[d.ID] = d.Fqdn
	}
	out := make([]gin.H, 0, len(aliases))
	for _, a := range aliases {
		address := a.LocalPart + "@" + byID[a.DomainID]
		if a.Kind == "catchall" {
			address = "@" + byID[a.DomainID]
		}
		out = append(out, gin.H{"address": address, "forward_to": a.Target, "kind": a.Kind})
	}
	c.JSON(http.StatusOK, out)
}

// AddMailUser creates a mailbox through the compatibility API.
func (h *LegacyHandler) AddMailUser(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(c.PostForm("email")))
	password := c.PostForm("password")
	quota, _ := strconv.ParseInt(c.PostForm("quota"), 10, 64)
	if quota < 0 {
		quota = 0
	}
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at == len(email)-1 || password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	local, fqdn := email[:at], email[at+1:]
	domain, err := h.Store.GetDomainByFQDN(c, store.GetDomainByFQDNParams{TenantID: h.TenantID, Fqdn: fqdn})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain not found"})
		return
	}
	hash, err := mail.HashPassword(password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash"})
		return
	}
	box, err := h.Store.CreateMailbox(c, store.CreateMailboxParams{
		TenantID:     h.TenantID,
		DomainID:     domain.ID,
		LocalPart:    local,
		Address:      email,
		PasswordHash: hash,
		QuotaBytes:   quota,
		Status:       "active",
		SendEnabled:  true,
	})
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "mailbox exists"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"email": box.Address, "status": box.Status})
}

// AddMailAlias creates an alias through the compatibility API.
func (h *LegacyHandler) AddMailAlias(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	address := strings.ToLower(strings.TrimSpace(c.PostForm("address")))
	target := strings.ToLower(strings.TrimSpace(c.PostForm("forward_to")))
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || at == len(address)-1 || target == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	domain, err := h.Store.GetDomainByFQDN(c, store.GetDomainByFQDNParams{
		TenantID: h.TenantID,
		Fqdn:     address[at+1:],
	})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain not found"})
		return
	}
	alias, err := h.Store.CreateAlias(c, store.CreateAliasParams{
		TenantID:  h.TenantID,
		DomainID:  domain.ID,
		LocalPart: address[:at],
		Target:    target,
		Kind:      "alias",
		Active:    true,
	})
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "alias exists"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"address": alias.LocalPart + "@" + domain.Fqdn, "forward_to": alias.Target})
}

// RemoveMailUser removes a mailbox subject to database safety guards.
func (h *LegacyHandler) RemoveMailUser(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(c.PostForm("email")))
	box, err := h.Store.GetMailboxByAddress(c, store.GetMailboxByAddressParams{TenantID: h.TenantID, Address: email})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "mailbox not found"})
		return
	}
	if err := h.Store.DeleteMailbox(c, store.DeleteMailboxParams{TenantID: h.TenantID, ID: box.ID}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// SetMailUserPassword changes a mailbox password after legacy authorization.
func (h *LegacyHandler) SetMailUserPassword(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(c.PostForm("email")))
	password := c.PostForm("password")
	if password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	box, err := h.Store.GetMailboxByAddress(c, store.GetMailboxByAddressParams{TenantID: h.TenantID, Address: email})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "mailbox not found"})
		return
	}
	hash, err := mail.HashPassword(password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash"})
		return
	}
	if _, err := h.Store.UpdateMailboxPassword(c, store.UpdateMailboxPasswordParams{
		TenantID: h.TenantID, ID: box.ID, PasswordHash: hash,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// RemoveMailAlias removes an alias owned by the configured tenant.
func (h *LegacyHandler) RemoveMailAlias(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	address := strings.ToLower(strings.TrimSpace(c.PostForm("address")))
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || at == len(address)-1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	domain, err := h.Store.GetDomainByFQDN(c, store.GetDomainByFQDNParams{TenantID: h.TenantID, Fqdn: address[at+1:]})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain not found"})
		return
	}
	aliases, err := h.Store.ListAliasesByTenant(c, h.TenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db"})
		return
	}
	for _, alias := range aliases {
		if alias.DomainID == domain.ID && alias.LocalPart == address[:at] {
			if err := h.Store.DeleteAlias(c, store.DeleteAliasParams{TenantID: h.TenantID, ID: alias.ID}); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "alias not found"})
}

// SSLStatus reports certificates from the managed certificate directory.
func (h *LegacyHandler) SSLStatus(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(certs.DomainCertDir, "*.cert"))
	out := make([]gin.H, 0, len(matches))
	for _, path := range matches {
		raw, err := os.ReadFile(path) //nolint:gosec // Fixed managed certificate glob or validated hostname under a fixed zone root.
		if err != nil {
			continue
		}
		block, _ := pem.Decode(raw)
		if block == nil {
			continue
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		out = append(out, gin.H{
			"domain":      strings.TrimSuffix(filepath.Base(path), ".cert"),
			"not_after":   leaf.NotAfter.UTC().Format(time.RFC3339),
			"issuer":      leaf.Issuer.CommonName,
			"self_signed": leaf.Issuer.CommonName == leaf.Subject.CommonName,
		})
	}
	c.JSON(http.StatusOK, out)
}

// ZoneFile returns a validated managed DNS zone file.
func (h *LegacyHandler) ZoneFile(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	zone := strings.ToLower(strings.TrimSpace(c.Param("zone")))
	if !validation.IsValidHostname(zone) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid zone"})
		return
	}
	path := filepath.Join("/etc/nsd/zones", zone+".txt")
	raw, err := os.ReadFile(path) //nolint:gosec // Fixed managed certificate glob or validated hostname under a fixed zone root.
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "zone not found"})
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", raw)
}

// Updates reports the supported compatibility update response.
func (h *LegacyHandler) Updates(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"updates": []string{}})
}

// BackupStatus lists backup metadata without backup contents.
func (h *LegacyHandler) BackupStatus(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	entries, _ := os.ReadDir("/var/backups/urmail")
	out := make([]gin.H, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, gin.H{
			"name":     entry.Name(),
			"size":     info.Size(),
			"modified": info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"backups": out})
}

// WebDomains lists the configured tenant's web domains.
func (h *LegacyHandler) WebDomains(c *gin.Context) {
	h.MailDomains(c)
}

// MFAStatus reports the authenticated user's actual MFA state.
func (h *LegacyHandler) MFAStatus(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	userID, _ := middleware.GetUserID(c)
	reader, ok := h.Auth.(interface {
		GetUserByID(context.Context, int64) (store.GetUserByIDRow, error)
	})
	if !ok {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	user, err := reader.GetUserByID(c, userID)
	if err != nil {
		authError(c, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}
	c.JSON(http.StatusOK, gin.H{"mfa_enabled": user.MfaEnabled})
}

// LatestVersion reports the compatibility version without an upstream request.
func (h *LegacyHandler) LatestVersion(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": h.Version})
}

// Privacy returns the supported compatibility privacy response.
func (h *LegacyHandler) Privacy(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	c.JSON(http.StatusOK, []gin.H{})
}

// EmptyList returns an authenticated empty capability list.
func (h *LegacyHandler) EmptyList(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	c.JSON(http.StatusOK, []gin.H{})
}

// Unsupported rejects an explicitly unsupported compatibility operation.
func (h *LegacyHandler) Unsupported(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	c.JSON(http.StatusNotImplemented, gin.H{"error": "not supported by urMail.cn"})
}
