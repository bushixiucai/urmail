package handlers

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/store"
)

// DNSAuthorityAgent permits only the structured agent request used below.
type DNSAuthorityAgent interface {
	Do(context.Context, string, any) (*agentd.Response, error)
}

// DNSAuthorityHandler joins tenant authorization to fixed authoritative DNS operations.
type DNSAuthorityHandler struct {
	App       *db.Pool
	Agent     DNSAuthorityAgent
	Reconcile ReconcileTrigger
}

// RegisterDNSAuthorityRoutes adds owner/admin-only authority lifecycle endpoints.
func RegisterDNSAuthorityRoutes(group *gin.RouterGroup, h *DNSAuthorityHandler) {
	group.GET("/domains/:id/authority", middleware.RequireRole("owner", "admin"), h.Status)
	group.POST("/domains/:id/authority", middleware.RequireRole("owner", "admin"), h.Manage)
}
func (h *DNSAuthorityHandler) domain(c *gin.Context) (store.GetDomainByIDRow, bool) {
	var empty store.GetDomainByIDRow
	tenant, ok := marketingTenant(c)
	if !ok {
		return empty, false
	}
	id, ok := marketingID(c, "id")
	if !ok {
		return empty, false
	}
	if h.App == nil || h.Agent == nil {
		authError(c, 503, "DNS_AUTHORITY_UNAVAILABLE")
		return empty, false
	}
	domain, err := requestStore(c, store.New(h.App)).GetDomainByID(c.Request.Context(), store.GetDomainByIDParams{TenantID: tenant, ID: id})
	if supportError(c, err) {
		return empty, false
	}
	if domain.Mode != "managed" || domain.Status != "active" || !domain.VerifiedAt.Valid {
		authError(c, 409, "DNS_AUTHORITY_MANAGED_REQUIRED")
		return empty, false
	}
	return domain, true
}

// Status obtains live signer, delegation and secondary evidence from the agent.
func (h *DNSAuthorityHandler) Status(c *gin.Context) {
	domain, ok := h.domain(c)
	if !ok {
		return
	}
	actor, _ := middleware.GetUserID(c)
	params := agentd.DNSAuthorityParams{TenantID: domain.TenantID, DomainID: domain.ID, ActorID: actor, Domain: domain.Fqdn}
	// Never hold a tenant transaction while an agent operation needs platform DB access.
	middleware.AfterCommit(c, func() { h.call(c, agentd.OpDNSAuthorityStatus, params, false) })
}

// Manage requires explicit confirmation and never accepts paths, key material or shell commands.
func (h *DNSAuthorityHandler) Manage(c *gin.Context) {
	domain, ok := h.domain(c)
	if !ok {
		return
	}
	actor, _ := middleware.GetUserID(c)
	var in struct {
		Action    string               `json:"action"`
		Confirm   bool                 `json:"confirm"`
		Secondary *nsd.SecondaryConfig `json:"secondary,omitempty"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(&in) != nil || decoder.Decode(&trailing) != io.EOF || !in.Confirm {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	allowed := map[string]bool{"prepare": true, "enable": true, "disable-check": true, "disable": true, "rotate": true, "retire": true, "rollback": true, "secondary-configure": true, "secondary-prepare": true, "backup-manifest": true}
	if !allowed[in.Action] || (in.Secondary != nil && in.Action != "secondary-configure") {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	params := agentd.DNSAuthorityParams{TenantID: domain.TenantID, DomainID: domain.ID, ActorID: actor, Domain: domain.Fqdn, Action: in.Action, Secondary: in.Secondary}
	middleware.AfterCommit(c, func() { h.call(c, agentd.OpDNSAuthorityManage, params, true) })
}
func (h *DNSAuthorityHandler) call(c *gin.Context, op string, params agentd.DNSAuthorityParams, mutation bool) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	response, err := h.Agent.Do(ctx, op, params)
	if err != nil {
		code := "DNS_AUTHORITY_REQUIRES_REVIEW"
		if response == nil || response.Error == nil {
			code = "DNS_AUTHORITY_UNAVAILABLE"
		}
		authError(c, 409, code)
		return
	}
	if response == nil || !response.OK || !json.Valid([]byte(response.Stdout)) {
		authError(c, 503, "DNS_AUTHORITY_UNAVAILABLE")
		return
	}
	var result json.RawMessage
	if json.Unmarshal([]byte(response.Stdout), &result) == nil {
		if mutation && h.Reconcile != nil {
			h.Reconcile.Trigger()
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(200, result)
		return
	}
	authError(c, 503, "DNS_AUTHORITY_UNAVAILABLE")
}
