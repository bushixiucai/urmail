package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

type DomainStore interface {
	ListDomainsByTenant(ctx context.Context, tenantID int64) ([]store.ListDomainsByTenantRow, error)
	GetDomainByID(ctx context.Context, arg store.GetDomainByIDParams) (store.GetDomainByIDRow, error)
	CreateDomain(ctx context.Context, arg store.CreateDomainParams) (store.CreateDomainRow, error)
	DeleteDomain(ctx context.Context, arg store.DeleteDomainParams) error
	CountDomainsByTenant(ctx context.Context, tenantID int64) (int64, error)
	GetActiveSubscriptionByTenant(ctx context.Context, tenantID int64) (store.Subscriptions, error)
	GetPlanByCode(ctx context.Context, code string) (store.Plans, error)
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

// DomainResponse is the explicit public shape of a domain. It deliberately
// mirrors only a subset of the store row: the DKIM private key and the
// verification token are server-side data and must never cross the HTTP
// boundary.
type DomainResponse struct {
	ID        int64              `json:"id"`
	FQDN      string             `json:"fqdn"`
	Mode      string             `json:"mode"`
	Status    string             `json:"status"`
	Selector  string             `json:"selector"`
	CreatedAt pgtype.Timestamptz `json:"created_at"`
}

// DNSGuidanceResponse is the copy-paste DNS record set returned when a domain
// is created. Every value here is public; the DKIM entry is the published
// public-key TXT record, never the private key.
type DNSGuidanceResponse struct {
	MX    string `json:"mx"`
	SPF   string `json:"spf"`
	DKIM  string `json:"dkim"`
	DMARC string `json:"dmarc"`
}

func newDomainResponse(id int64, fqdn, mode, status, selector string, createdAt pgtype.Timestamptz) DomainResponse {
	return DomainResponse{
		ID:        id,
		FQDN:      fqdn,
		Mode:      mode,
		Status:    status,
		Selector:  selector,
		CreatedAt: createdAt,
	}
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

	items := make([]DomainResponse, 0, len(domains))
	for _, d := range domains {
		items = append(items, newDomainResponse(d.ID, d.Fqdn, d.Mode, d.Status, d.DkimSelector, d.CreatedAt))
	}

	c.JSON(http.StatusOK, gin.H{"domains": items})
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

	if !quotaCheck(c, requestStore[QuotaStore](c, nil), tenantID, "domain") {
		return
	}

	// Generate random verification token
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR"}})
		return
	}
	verifToken := hex.EncodeToString(tokenBytes)

	// Generate the DKIM keypair before inserting so the private key is
	// persisted together with the domain. Dropping the key would strand the
	// DNS TXT record published in the guidance below.
	dkim, err := domainMail.GenerateDKIMKeypair("mail")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR"}})
		return
	}

	domain, err := q.CreateDomain(c, store.CreateDomainParams{
		TenantID:          tenantID,
		Fqdn:              fqdn,
		Mode:              mode,
		Status:            "active",
		VerificationToken: verifToken,
		TlsMode:           "letsencrypt",
		DkimSelector:      dkim.Selector,
		DkimPrivateKey:    pgtype.Text{String: dkim.PrivateKey, Valid: true},
	})
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{"code": "DOMAIN_CONFLICT", "message": "domain.already_exists"},
		})
		return
	}

	// Trigger async reconcile pipeline
	if h.Reconcile != nil {
		h.Reconcile.Trigger()
	}

	c.JSON(http.StatusCreated, gin.H{
		"domain": newDomainResponse(
			domain.ID, domain.Fqdn, domain.Mode, domain.Status, domain.DkimSelector, domain.CreatedAt,
		),
		"dns_guidance": DNSGuidanceResponse{
			MX:    "10 box.urmail.cn.",
			SPF:   "v=spf1 mx -all",
			DKIM:  dkim.DNSTXT,
			DMARC: "v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r;",
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
