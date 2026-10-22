// Package handlers: legacy Mail-in-a-Box compatibility surface.
//
// The old /admin API is intentionally small: it exposes the read paths used
// by the legacy UI plus mailbox/alias creation. Authentication is HTTP Basic
// with a platform administrator account; every route is tenant-scoped to the
// first tenant, matching the single-tenant assumption of the old API.
package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

type LegacyAuthStore interface {
	GetUserByEmail(ctx context.Context, email string) (store.GetUserByEmailRow, error)
	CheckPlatformAdmin(ctx context.Context, userID int64) (bool, error)
}

type LegacyStore interface {
	ListMailboxesByTenant(ctx context.Context, tenantID int64) ([]store.Mailboxes, error)
	ListDomainsByTenant(ctx context.Context, tenantID int64) ([]store.ListDomainsByTenantRow, error)
	ListAliasesByTenant(ctx context.Context, tenantID int64) ([]store.ListAliasesByTenantRow, error)
	GetDomainByFQDN(ctx context.Context, arg store.GetDomainByFQDNParams) (store.GetDomainByFQDNRow, error)
	CreateMailbox(ctx context.Context, arg store.CreateMailboxParams) (store.Mailboxes, error)
	CreateAlias(ctx context.Context, arg store.CreateAliasParams) (store.MailboxAliases, error)
}

type LegacyHandler struct {
	Auth     LegacyAuthStore
	Store    LegacyStore
	TenantID int64
	Version  string
}

func NewLegacyHandler(auth LegacyAuthStore, s LegacyStore, tenantID int64, version string) *LegacyHandler {
	if tenantID <= 0 {
		tenantID = 1
	}
	return &LegacyHandler{Auth: auth, Store: s, TenantID: tenantID, Version: version}
}

func (h *LegacyHandler) authorize(c *gin.Context) bool {
	email, password, ok := c.Request.BasicAuth()
	if !ok {
		c.Header("WWW-Authenticate", `Basic realm="urMail"`)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return false
	}
	user, err := h.Auth.GetUserByEmail(c, strings.ToLower(email))
	if err != nil || !mail.VerifyPassword(password, user.PasswordHash) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return false
	}
	admin, err := h.Auth.CheckPlatformAdmin(c, user.ID)
	if err != nil || !admin {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return false
	}
	return true
}

func (h *LegacyHandler) Status(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "version": h.Version})
}

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

func (h *LegacyHandler) DNSZones(c *gin.Context) {
	h.MailDomains(c)
}

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
