package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/dnsprovider"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/onboarding"
	"github.com/bushixiucai/urmail/internal/store"
)

const dnsPreparedKey = "urmail.dns.prepared"

type preparedDNSCredential struct {
	Tenant, Domain                 int64
	Provider, Zone, ZoneID, Cipher string
}
type preparedDNSPlan struct {
	Tenant, Domain int64
	Draft          onboarding.Draft
}
type preparedDNSObservation struct {
	Tenant, Domain int64
	ID             pgtype.UUID
	Fence          int64
	Reason         string
}

// Preflight must run after authentication/CSRF/rate limiting and before
// RequireTenantTx. Provider reads finish before the business lock is acquired;
// only server-held ciphertext/public snapshots cross into the audited TX.
func (h *DNSPlanHandler) Preflight(c *gin.Context) {
	if !h.prepareDNS(c) {
		c.Abort()
		return
	}
	c.Next()
}
func (h *DNSPlanHandler) prepareDNS(c *gin.Context) bool {
	if _, exists := c.Get(middleware.ContextQueries); exists {
		authError(c, http.StatusServiceUnavailable, "DNS_PREPARATION_REQUIRED")
		return false
	}
	tenant, domain, ok := h.tenantDomain(c)
	if !ok {
		return false
	}
	uid, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, http.StatusForbidden, "FORBIDDEN")
		return false
	}
	// This completed scalar query holds no transaction or advisory lock while
	// calling the provider. RequireTenantTx checks the current role again later.
	role, err := store.New(h.Service.Pool.Pool).GetUserRoleForTenant(c, store.GetUserRoleForTenantParams{UserID: uid, TenantID: tenant})
	if err != nil || (role != "owner" && role != "admin") {
		authError(c, http.StatusForbidden, "FORBIDDEN")
		return false
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	switch {
	case c.Request.Method == http.MethodPut && strings.HasSuffix(c.FullPath(), "/dns/credentials"):
		var input struct {
			Provider    string                  `json:"provider"`
			Zone        string                  `json:"zone"`
			Credentials dnsprovider.Credentials `json:"credentials"`
		}
		if c.ShouldBindJSON(&input) != nil || h.Service.Secrets == nil {
			authError(c, http.StatusBadRequest, "DNS_CREDENTIAL_INVALID")
			return false
		}
		input.Zone = strings.TrimSpace(input.Zone)
		// MAIL-004: a provider zone may be typed in Unicode form. Normalize it
		// to the same lowercase A-label used for domains.fqdn so the ownership
		// comparison below cannot be bypassed or broken by an alias spelling.
		zoneName, err := validation.NormalizeDomain(input.Zone)
		if err != nil {
			authError(c, http.StatusBadRequest, "DNS_CREDENTIAL_INVALID")
			return false
		}
		input.Zone = zoneName
		d, err := store.New(h.Service.Pool.Pool).PlatformDNSDomain(ctx, store.PlatformDNSDomainParams{TenantID: tenant, ID: domain})
		if err != nil || d.Status != "active" || !d.VerifiedAt.Valid || (d.Fqdn != input.Zone && !strings.HasSuffix(d.Fqdn, "."+input.Zone)) {
			authError(c, http.StatusForbidden, "DNS_ZONE_UNAUTHORIZED")
			return false
		}
		manager, err := h.Service.NewManager(input.Provider, input.Credentials)
		if err != nil {
			authError(c, http.StatusBadRequest, "DNS_CREDENTIAL_INVALID")
			return false
		}
		zone, err := manager.Probe(ctx, input.Zone)
		if err != nil || zone.Name != input.Zone {
			authError(c, http.StatusConflict, "DNS_ZONE_UNAVAILABLE")
			return false
		}
		raw, err := json.Marshal(input.Credentials)
		if err != nil {
			authError(c, http.StatusBadRequest, "DNS_CREDENTIAL_INVALID")
			return false
		}
		cipher, err := h.Service.Secrets.EncryptString(string(raw))
		if err != nil {
			authError(c, http.StatusServiceUnavailable, "DNS_UNAVAILABLE")
			return false
		}
		c.Set(dnsPreparedKey, preparedDNSCredential{tenant, domain, input.Provider, zone.Name, zone.ID, cipher})
		return true
	case c.Request.Method == http.MethodPost && strings.HasSuffix(c.FullPath(), "/dns/plans"):
		var input struct {
			Kind            string                     `json:"kind"`
			Imported        []dnsprovider.RemoteRecord `json:"imported_records"`
			ReviewedWebsite bool                       `json:"reviewed_website_records"`
		}
		if c.ShouldBindJSON(&input) != nil || len(input.Imported) > 1000 || (input.Kind == "managed" && !input.ReviewedWebsite) {
			authError(c, http.StatusBadRequest, "DNS_IMPORT_REVIEW_REQUIRED")
			return false
		}
		draft, err := h.Service.Prepare(ctx, tenant, domain, input.Kind, input.Imported)
		if err != nil {
			dnsError(c, err)
			return false
		}
		c.Set(dnsPreparedKey, preparedDNSPlan{tenant, domain, draft})
		return true
	case c.Request.Method == http.MethodPost && strings.HasSuffix(c.FullPath(), "/reconcile"):
		id, ok := dnsPlanID(c)
		if !ok {
			return false
		}
		var input struct {
			Reason  string `json:"reason"`
			Confirm bool   `json:"confirm_observed"`
		}
		if c.ShouldBindJSON(&input) != nil || !input.Confirm || len(strings.TrimSpace(input.Reason)) < 10 {
			authError(c, http.StatusBadRequest, "DNS_REVIEW_REQUIRED")
			return false
		}
		p, err := store.New(h.Service.Pool.Pool).GetDNSPlan(ctx, store.GetDNSPlanParams{TenantID: tenant, ID: id})
		if err != nil || p.DomainID != domain || p.Status != "uncertain" {
			authError(c, http.StatusNotFound, "NOT_FOUND")
			return false
		}
		if err = h.Service.ObservePlan(ctx, p); err != nil {
			dnsError(c, err)
			return false
		}
		c.Set(dnsPreparedKey, preparedDNSObservation{tenant, domain, id, p.Fence, input.Reason})
		return true
	default:
		authError(c, http.StatusServiceUnavailable, "DNS_PREPARATION_REQUIRED")
		return false
	}
}
