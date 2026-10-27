package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

// DomainStore exposes tenant-scoped domain persistence.
type DomainStore interface {
	RotateDomainChallenge(context.Context, store.RotateDomainChallengeParams) (store.RotateDomainChallengeRow, error)
	VerifyDomainChallenge(context.Context, store.VerifyDomainChallengeParams) (store.VerifyDomainChallengeRow, error)
	ListDomainsByTenant(ctx context.Context, tenantID int64) ([]store.ListDomainsByTenantRow, error)
	GetDomainByID(ctx context.Context, arg store.GetDomainByIDParams) (store.GetDomainByIDRow, error)
	CreateDomain(ctx context.Context, arg store.CreateDomainParams) (store.CreateDomainRow, error)
	UpdateDomainStatus(ctx context.Context, arg store.UpdateDomainStatusParams) (store.UpdateDomainStatusRow, error)
	DeleteDomain(ctx context.Context, arg store.DeleteDomainParams) error
	CountDomainsByTenant(ctx context.Context, tenantID int64) (int64, error)
	GetActiveSubscriptionByTenant(ctx context.Context, tenantID int64) (store.Subscriptions, error)
	GetPlanByCode(ctx context.Context, code string) (store.Plans, error)
	UpdateDomainModeStatus(ctx context.Context, arg store.UpdateDomainModeStatusParams) (store.UpdateDomainModeStatusRow, error)
}

// ReconcileTrigger schedules configuration regeneration after a commit.
type ReconcileTrigger interface {
	Trigger()
}

// DNSProvider publishes domain verification records.
type DNSProvider interface {
	EnsureTXT(ctx context.Context, zone, name, value string) error
}

// DomainHandler manages tenant domains and verification.
type DomainHandler struct {
	Store     DomainStore
	Reconcile ReconcileTrigger
	DNS       DNSProvider
	// Hostname is the MX target advertised in DNS guidance. It is injected
	// from config so a non-default deployment does not publish box.urmail.cn.
	Hostname string
	// DNSManagedZones lists the DNS zones the operator's provider credentials
	// are allowed to maintain after independent tenant ownership proof.
	// This list never authorizes writing an ownership challenge.
	DNSManagedZones []string
	// LookupTXT is the ownership resolver; nil uses the system resolver.
	LookupTXT func(context.Context, string) ([]string, error)
}

func (h *DomainHandler) mxHost() string {
	if h.Hostname != "" {
		return h.Hostname
	}
	return "box.urmail.cn"
}

// NewDomainHandler serves the tenant management operation.
func NewDomainHandler(s DomainStore, r ReconcileTrigger) *DomainHandler {
	return &DomainHandler{Store: s, Reconcile: r}
}

// DomainResponse is the explicit public shape of a domain. It deliberately
// mirrors only a subset of the store row: the DKIM private key and the
// verification token are server-side data and must never cross the HTTP
// boundary.
//
// FQDN is the canonical wire form (lowercase A-label) used for storage, DNS,
// certificates and the mail maps. DisplayFQDN is the U-label form for user
// display only; no server-side input may be built from it (MAIL-004).
type DomainResponse struct {
	ID                int64              `json:"id"`
	FQDN              string             `json:"fqdn"`
	DisplayFQDN       string             `json:"display_fqdn"`
	Mode              string             `json:"mode"`
	Status            string             `json:"status"`
	Selector          string             `json:"selector"`
	CreatedAt         pgtype.Timestamptz `json:"created_at"`
	ProvisioningState string             `json:"provisioning_state"`
}

// DNSGuidanceResponse is the copy-paste DNS record set returned when a domain
// is created. Every value here is public; the DKIM entry is the published
// public-key TXT record, never the private key.
type DNSGuidanceResponse struct {
	MX                string `json:"mx"`
	SPF               string `json:"spf"`
	DKIM              string `json:"dkim"`
	DMARC             string `json:"dmarc"`
	VerificationName  string `json:"verification_name"`
	VerificationValue string `json:"verification_value"`
}

func newDomainResponse(id int64, fqdn, mode, status, selector string, createdAt pgtype.Timestamptz, provisioning ...string) DomainResponse {
	state := "pending"
	if len(provisioning) > 0 && provisioning[0] != "" {
		state = provisioning[0]
	}
	return DomainResponse{
		ProvisioningState: state,
		ID:                id,
		FQDN:              fqdn,
		DisplayFQDN:       displayFQDN(fqdn),
		Mode:              mode,
		Status:            status,
		Selector:          selector,
		CreatedAt:         createdAt,
	}
}

// displayFQDN renders a stored A-label as its U-label form. A row that no
// longer passes the IDNA boundary (legacy or hand-edited data) falls back to
// the stored value so one bad row cannot fail a list or update response.
func displayFQDN(fqdn string) string {
	display, err := validation.DomainToUnicode(fqdn)
	if err != nil || display == "" {
		return fqdn
	}
	return display
}

// List serves the tenant management operation.
func (h *DomainHandler) List(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	domains, err := q.ListDomainsByTenant(c, tenantID)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}

	items := make([]DomainResponse, 0, len(domains))
	for _, d := range domains {
		items = append(items, newDomainResponse(d.ID, d.Fqdn, d.Mode, d.Status, d.DkimSelector, d.CreatedAt, d.ProvisioningState))
	}

	c.JSON(http.StatusOK, gin.H{"domains": items})
}

// CreateDomainRequest defines the tenant management contract.
type CreateDomainRequest struct {
	FQDN string `json:"fqdn" binding:"required"`
	Mode string `json:"mode"` // managed or delegated (default)
}

// Create serves the tenant management operation.
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

	// MAIL-004: normalize at the boundary instead of rejecting Unicode input.
	// Every accepted spelling of one domain (U-label, A-label, mixed case,
	// trailing dot, alternate label separators) becomes the same lowercase
	// A-label, so the domains.fqdn unique key can only ever hold one row.
	// A full address is not a domain and must never be split here.
	fqdn, err := validation.NormalizeDomain(req.FQDN)
	if err != nil || strings.Contains(req.FQDN, "@") {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "INVALID_FQDN", "message": "domain.invalid_fqdn"},
		})
		return
	}

	mode := req.Mode
	if mode == "" {
		mode = "delegated"
	}
	if mode != "managed" && mode != "delegated" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
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
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR"}})
		return
	}

	domain, err := q.CreateDomain(c, store.CreateDomainParams{
		TenantID:          tenantID,
		Fqdn:              fqdn,
		Mode:              mode,
		Status:            "pending_dns",
		VerificationToken: verifToken,
		TlsMode:           "letsencrypt",
		DkimSelector:      dkim.Selector,
		DkimPrivateKey:    pgtype.Text{String: dkim.PrivateKey, Valid: true},
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{"code": "DOMAIN_CONFLICT", "message": "domain.already_exists"},
		})
		return
	}

	// A platform DNS credential cannot prove the requesting tenant owns a
	// domain. Never publish the ownership challenge on the tenant's behalf.
	// Automatic record maintenance can begin only after independent proof.
	dnsAuto := false

	// Trigger async reconcile pipeline
	if h.Reconcile != nil {
		middleware.AfterCommit(c, h.Reconcile.Trigger)
	}

	c.JSON(http.StatusCreated, gin.H{
		"domain": newDomainResponse(
			domain.ID, domain.Fqdn, domain.Mode, domain.Status, domain.DkimSelector, domain.CreatedAt, domain.ProvisioningState,
		),
		"dns_auto": dnsAuto,
		"dns_guidance": DNSGuidanceResponse{
			MX:                fmt.Sprintf("10 %s.", h.mxHost()),
			SPF:               "v=spf1 mx -all",
			DKIM:              dkim.DNSTXT,
			DMARC:             "v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r;",
			VerificationName:  "_urmail-verify." + fqdn,
			VerificationValue: verifToken,
		},
	})
}

// Verify checks the per-domain TXT record and activates the domain. A
// pending_dns domain is not rendered into Postfix/NSD/nginx until this
// succeeds, so a tenant cannot claim a domain it does not control.
func (h *DomainHandler) Verify(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	domain, err := q.GetDomainByID(c, store.GetDomainByIDParams{TenantID: tenantID, ID: id})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "DOMAIN_NOT_FOUND"}})
		return
	}
	if domain.Status == "active" {
		c.JSON(http.StatusOK, gin.H{"domain": newDomainResponse(domain.ID, domain.Fqdn, domain.Mode, domain.Status, domain.DkimSelector, domain.CreatedAt, domain.ProvisioningState)})
		return
	}
	// Challenge expiry is independent of domain creation. A renewed challenge
	// can recover an old domain without deleting retained mailbox data.
	now := time.Now().UTC()
	if !domain.VerificationIssuedAt.Valid || !domain.VerificationExpiresAt.Valid || !now.Before(domain.VerificationExpiresAt.Time) || domain.VerificationIssuedAt.Time.After(now.Add(time.Minute)) {
		c.JSON(http.StatusGone, gin.H{"error": gin.H{"code": "DOMAIN_CHALLENGE_EXPIRED", "message": "domain.challenge_expired"}})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	lookup := h.LookupTXT
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	records, err := lookup(ctx, "_urmail-verify."+domain.Fqdn)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "DOMAIN_VERIFICATION_FAILED", "message": "domain.verification_failed"},
		})
		return
	}
	verified := false
	for _, record := range records {
		if strings.TrimSpace(record) == domain.VerificationToken {
			verified = true
			break
		}
	}
	if !verified {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "DOMAIN_NOT_VERIFIED", "message": "domain.verification_required"},
		})
		return
	}

	updated, err := q.VerifyDomainChallenge(c, store.VerifyDomainChallengeParams{TenantID: tenantID, ID: domain.ID, VerificationToken: domain.VerificationToken})
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "DOMAIN_CHALLENGE_CHANGED", "message": "domain.challenge_changed"}})
		return
	}
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if h.Reconcile != nil {
		middleware.AfterCommit(c, h.Reconcile.Trigger)
	}
	c.JSON(http.StatusOK, gin.H{"domain": newDomainResponse(updated.ID, updated.Fqdn, updated.Mode, updated.Status, updated.DkimSelector, updated.CreatedAt, updated.ProvisioningState)})
}

// ReissueChallenge rotates only a pending domain's ownership proof. The
// previous token is invalid immediately, even if its DNS lookup is in flight.
func (h *DomainHandler) ReissueChallenge(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_ID")
		return
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		authError(c, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	challenge, err := requestStore(c, h.Store).RotateDomainChallenge(c, store.RotateDomainChallengeParams{TenantID: tenantID, ID: id, VerificationToken: hex.EncodeToString(random[:])})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusConflict, "DOMAIN_CHALLENGE_UNAVAILABLE")
		return
	}
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"verification_name": "_urmail-verify." + challenge.Fqdn, "verification_value": challenge.VerificationToken, "issued_at": challenge.VerificationIssuedAt, "expires_at": challenge.VerificationExpiresAt})
}

// Delete serves the tenant management operation.
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
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}

	if err := q.DeleteDomain(c, store.DeleteDomainParams{
		TenantID: tenantID,
		ID:       id,
	}); err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DELETE_FAILED"}})
		return
	}

	if h.Reconcile != nil {
		middleware.AfterCommit(c, h.Reconcile.Trigger)
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// UpdateDomainRequest defines the tenant management contract.
type UpdateDomainRequest struct {
	Mode   string `json:"mode"`
	Status string `json:"status"`
}

// Update serves the tenant management operation.
func (h *DomainHandler) Update(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	current, err := q.GetDomainByID(c, store.GetDomainByIDParams{TenantID: tenantID, ID: id})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "DOMAIN_NOT_FOUND"}})
		return
	}
	var req UpdateDomainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = current.Mode
	}
	status := req.Status
	if status == "" {
		status = current.Status
	}
	if mode != "managed" && mode != "delegated" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	if status != "pending_dns" && status != "active" && status != "disabled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	if status == "active" && !current.VerifiedAt.Valid {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "DOMAIN_NOT_VERIFIED"}})
		return
	}
	updated, err := q.UpdateDomainModeStatus(c, store.UpdateDomainModeStatusParams{
		TenantID: tenantID, ID: id, Mode: mode, Status: status,
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if h.Reconcile != nil {
		middleware.AfterCommit(c, h.Reconcile.Trigger)
	}
	c.JSON(http.StatusOK, gin.H{"domain": newDomainResponse(
		updated.ID, updated.Fqdn, updated.Mode, updated.Status, updated.DkimSelector, updated.CreatedAt, updated.ProvisioningState,
	)})
}
