package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

type AliasStore interface {
	ListAliasesByTenant(ctx context.Context, tenantID int64) ([]store.ListAliasesByTenantRow, error)
	ListMailboxesByTenant(ctx context.Context, tenantID int64) ([]store.Mailboxes, error)
	GetDomainByID(ctx context.Context, arg store.GetDomainByIDParams) (store.GetDomainByIDRow, error)
	CreateAlias(ctx context.Context, arg store.CreateAliasParams) (store.MailboxAliases, error)
	DeleteAlias(ctx context.Context, arg store.DeleteAliasParams) error
}

type AliasHandler struct {
	Store     AliasStore
	Reconcile ReconcileTrigger
}

func NewAliasHandler(s AliasStore, r ReconcileTrigger) *AliasHandler {
	return &AliasHandler{Store: s, Reconcile: r}
}

// AliasResponse is the explicit public shape of an alias. tenant_id is
// implied by the request context, so it is not repeated in the body.
type AliasResponse struct {
	ID         int64              `json:"id"`
	DomainID   int64              `json:"domain_id"`
	DomainFQDN string             `json:"domain_fqdn"`
	LocalPart  string             `json:"local_part"`
	Target     string             `json:"target"`
	Kind       string             `json:"kind"`
	Active     bool               `json:"active"`
	CreatedAt  pgtype.Timestamptz `json:"created_at"`
}

func newAliasResponse(id, domainID int64, domainFQDN, localPart, target, kind string, active bool, createdAt pgtype.Timestamptz) AliasResponse {
	return AliasResponse{
		ID:         id,
		DomainID:   domainID,
		DomainFQDN: domainFQDN,
		LocalPart:  localPart,
		Target:     target,
		Kind:       kind,
		Active:     active,
		CreatedAt:  createdAt,
	}
}

func (h *AliasHandler) List(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	aliases, err := q.ListAliasesByTenant(c, tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}

	items := make([]AliasResponse, 0, len(aliases))
	for _, a := range aliases {
		items = append(items, newAliasResponse(
			a.ID, a.DomainID, a.DomainFqdn, a.LocalPart, a.Target, a.Kind, a.Active, a.CreatedAt,
		))
	}

	c.JSON(http.StatusOK, gin.H{"aliases": items})
}

type CreateAliasRequest struct {
	DomainID  int64  `json:"domain_id" binding:"required"`
	LocalPart string `json:"local_part"` // empty for catchall
	Target    string `json:"target" binding:"required"`
	Kind      string `json:"kind"` // alias|catchall|forward
}

func (h *AliasHandler) Create(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	var req CreateAliasRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}

	domain, err := q.GetDomainByID(c, store.GetDomainByIDParams{
		TenantID: tenantID,
		ID:       req.DomainID,
	})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "DOMAIN_NOT_FOUND"}})
		return
	}

	kind := req.Kind
	if kind == "" {
		if req.LocalPart == "" {
			kind = "catchall"
		} else {
			kind = "alias"
		}
	}
	if kind != "alias" && kind != "catchall" && kind != "forward" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	if len(req.LocalPart) > 64 || len(req.Target) > 320 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}

	local := strings.ToLower(strings.TrimSpace(req.LocalPart))
	target := strings.ToLower(strings.TrimSpace(req.Target))

	// Validate rule against domainMail
	existingAliases, _ := q.ListAliasesByTenant(c, tenantID)
	existingMailboxes, _ := q.ListMailboxesByTenant(c, tenantID)

	mailboxAddrs := make([]string, 0, len(existingMailboxes))
	for _, m := range existingMailboxes {
		mailboxAddrs = append(mailboxAddrs, m.Address)
	}

	rules := make([]domainMail.AliasRule, 0, len(existingAliases)+1)
	for _, a := range existingAliases {
		rules = append(rules, domainMail.AliasRule{
			Domain:    a.DomainFqdn,
			LocalPart: a.LocalPart,
			Target:    a.Target,
			Kind:      a.Kind,
		})
	}
	// Add candidate rule
	rules = append(rules, domainMail.AliasRule{
		Domain:    domain.Fqdn,
		LocalPart: local,
		Target:    target,
		Kind:      kind,
	})

	if err := domainMail.ValidateAliases(rules, mailboxAddrs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "INVALID_ALIAS", "message": err.Error()},
		})
		return
	}

	alias, err := q.CreateAlias(c, store.CreateAliasParams{
		TenantID:  tenantID,
		DomainID:  domain.ID,
		LocalPart: local,
		Target:    target,
		Kind:      kind,
		Active:    true,
	})
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{"code": "ALIAS_CONFLICT", "message": "alias.already_exists"},
		})
		return
	}

	if h.Reconcile != nil {
		h.Reconcile.Trigger()
	}

	c.JSON(http.StatusCreated, gin.H{
		"alias": newAliasResponse(
			alias.ID, alias.DomainID, domain.Fqdn, alias.LocalPart, alias.Target, alias.Kind, alias.Active, alias.CreatedAt,
		),
	})
}

func (h *AliasHandler) Delete(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}

	if err := q.DeleteAlias(c, store.DeleteAliasParams{
		TenantID: tenantID,
		ID:       id,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DELETE_FAILED"}})
		return
	}

	if h.Reconcile != nil {
		h.Reconcile.Trigger()
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
