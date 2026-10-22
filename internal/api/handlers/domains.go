package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

type DomainStore interface {
	ListDomainsByTenant(ctx context.Context, tenantID int64) ([]store.Domains, error)
	GetDomainByID(ctx context.Context, arg store.GetDomainByIDParams) (store.Domains, error)
	CreateDomain(ctx context.Context, arg store.CreateDomainParams) (store.Domains, error)
	DeleteDomain(ctx context.Context, arg store.DeleteDomainParams) error
}

type ReconcileTrigger interface {
	Trigger()
}

type DomainHandler struct {
	Store     DomainStore
	Reconcile ReconcileTrigger
}

func NewDomainHandler(s DomainStore, r ReconcileTrigger) *DomainHandler {
	return &DomainHandler{Store: s, Reconcile: r}
}

func (h *DomainHandler) List(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	domains, err := q.ListDomainsByTenant(c, tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"domains": domains})
}

type CreateDomainRequest struct {
	FQDN string `json:"fqdn" binding:"required"`
	Mode string `json:"mode"` // managed or delegated (default)
}

func (h *DomainHandler) Create(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	var req CreateDomainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}

	fqdn := strings.ToLower(strings.TrimSpace(req.FQDN))
	if !validation.IsValidHostname(fqdn) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "INVALID_FQDN", "message": "domain.invalid_fqdn"},
		})
		return
	}

	mode := req.Mode
	if mode != "managed" && mode != "delegated" {
		mode = "delegated"
	}

	// Generate random verification token
	tokenBytes := make([]byte, 16)
	_, _ = rand.Read(tokenBytes)
	verifToken := hex.EncodeToString(tokenBytes)

	domain, err := q.CreateDomain(c, store.CreateDomainParams{
		TenantID:          tenantID,
		Fqdn:              fqdn,
		Mode:              mode,
		Status:            "active",
		VerificationToken: verifToken,
		TlsMode:           "letsencrypt",
	})
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{"code": "DOMAIN_CONFLICT", "message": "domain.already_exists"},
		})
		return
	}

	// Generate DKIM DNS record info
	dkimKey, _ := domainMail.GenerateDKIMKeypair("mail")
	var dkimTXT string
	if dkimKey != nil {
		dkimTXT = dkimKey.DNSTXT
	}

	// Trigger async reconcile pipeline
	if h.Reconcile != nil {
		h.Reconcile.Trigger()
	}

	c.JSON(http.StatusCreated, gin.H{
		"domain": domain,
		"dns_guidance": gin.H{
			"mx":    "10 box.urmail.cn.",
			"spf":   "v=spf1 mx -all",
			"dkim":  dkimTXT,
			"dmarc": "v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r;",
		},
	})
}

func (h *DomainHandler) Delete(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}

	if err := q.DeleteDomain(c, store.DeleteDomainParams{
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