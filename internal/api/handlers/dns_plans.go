package handlers

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/onboarding"
	"github.com/bushixiucai/urmail/internal/store"
)

// DNSPlanHandler exposes explicit DNS previews and writes encrypted tenant
// credentials through the request transaction and mandatory audit boundary.
type DNSPlanHandler struct {
	Store   *store.Queries
	Service *onboarding.Service
}

func (h *DNSPlanHandler) tenantDomain(c *gin.Context) (int64, int64, bool) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return 0, 0, false
	}
	domain, ok := marketingID(c, "id")
	if !ok {
		return 0, 0, false
	}
	if h.Service == nil || h.Service.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "DNS_UNAVAILABLE")
		return 0, 0, false
	}
	return tenant, domain, true
}
func dnsError(c *gin.Context, err error) {
	if respondConstraintError(c, err) {
		return
	}
	authError(c, http.StatusConflict, "DNS_PLAN_REQUIRES_REVIEW")
}

// Credentials lists safe metadata. Ciphertexts and credentials are never DTOs.
func (h *DNSPlanHandler) Credentials(c *gin.Context) {
	tenant, domain, ok := h.tenantDomain(c)
	if !ok {
		return
	}
	rows, err := requestStore(c, h.Store).ListDNSCredentials(c, store.ListDNSCredentialsParams{TenantID: tenant, DomainID: domain})
	if err != nil {
		dnsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"credentials": rows})
}

// SaveCredential probes the exact zone read-only before encrypted persistence.
func (h *DNSPlanHandler) SaveCredential(c *gin.Context) {
	tenant, domain, ok := h.tenantDomain(c)
	if !ok {
		return
	}
	value, exists := c.Get(dnsPreparedKey)
	proof, ok := value.(preparedDNSCredential)
	if !exists || !ok || proof.Tenant != tenant || proof.Domain != domain {
		authError(c, http.StatusServiceUnavailable, "DNS_PREPARATION_REQUIRED")
		return
	}
	_, err := requestStore(c, h.Store).SaveDNSCredential(c, store.SaveDNSCredentialParams{TenantID: tenant, DomainID: domain, Provider: proof.Provider, ZoneName: proof.Zone, ZoneID: proof.ZoneID, EncryptedSecret: proof.Cipher})
	if err != nil {
		dnsError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// RevokeCredential invalidates active plans and leases in the same transaction.
func (h *DNSPlanHandler) RevokeCredential(c *gin.Context) {
	tenant, domain, ok := h.tenantDomain(c)
	if !ok {
		return
	}
	if err := requestStore(c, h.Store).RevokeDNSCredential(c, store.RevokeDNSCredentialParams{TenantID: tenant, DomainID: domain}); err != nil {
		dnsError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Preview performs no external mutation and cannot confirm its own plan.
func (h *DNSPlanHandler) Preview(c *gin.Context) {
	tenant, domain, ok := h.tenantDomain(c)
	if !ok {
		return
	}
	value, exists := c.Get(dnsPreparedKey)
	proof, ok := value.(preparedDNSPlan)
	if !exists || !ok || proof.Tenant != tenant || proof.Domain != domain {
		authError(c, http.StatusServiceUnavailable, "DNS_PREPARATION_REQUIRED")
		return
	}
	draft := proof.Draft
	var err error
	var id pgtype.UUID
	id.Valid = true
	if _, err = rand.Read(id.Bytes[:]); err != nil {
		authError(c, http.StatusInternalServerError, "DNS_UNAVAILABLE")
		return
	}
	id.Bytes[6] = (id.Bytes[6] & 15) | 64
	id.Bytes[8] = (id.Bytes[8] & 63) | 128
	before, _ := json.Marshal(draft.Before)
	desired, _ := json.Marshal(draft.Desired)
	changes, _ := json.Marshal(draft.Changes)
	_, err = requestStore(c, h.Store).CreateDNSPlan(c, store.CreateDNSPlanParams{ID: id, TenantID: tenant, DomainID: domain, CredentialID: draft.CredentialID, CredentialVersion: draft.CredentialVersion, Kind: draft.Kind, DomainDigest: draft.DomainDigest, SourceDigest: draft.SourceDigest, BeforeRecords: before, DesiredRecords: desired, Changes: changes})
	if err != nil {
		dnsError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "source_digest": draft.SourceDigest, "before_records": draft.Before, "desired_records": draft.Desired, "changes": draft.Changes, "expires_in_seconds": 900})
}

// Plans returns bounded public record snapshots and progress.
func (h *DNSPlanHandler) Plans(c *gin.Context) {
	tenant, domain, ok := h.tenantDomain(c)
	if !ok {
		return
	}
	rows, err := requestStore(c, h.Store).ListDNSPlans(c, store.ListDNSPlansParams{TenantID: tenant, DomainID: domain})
	if err != nil {
		dnsError(c, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, p := range rows {
		items = append(items, gin.H{"id": p.ID, "domain_id": p.DomainID, "kind": p.Kind, "status": p.Status, "source_digest": p.SourceDigest, "before_records": json.RawMessage(p.BeforeRecords), "desired_records": json.RawMessage(p.DesiredRecords), "changes": json.RawMessage(p.Changes), "last_error_code": p.LastErrorCode, "expires_at": p.ExpiresAt, "created_at": p.CreatedAt, "updated_at": p.UpdatedAt})
	}
	c.JSON(http.StatusOK, gin.H{"plans": items})
}
func dnsPlanID(c *gin.Context) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if id.Scan(c.Param("plan")) != nil || !id.Valid {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return id, false
	}
	return id, true
}

// Confirm requires the displayed immutable snapshot digest and explicit intent.
func (h *DNSPlanHandler) Confirm(c *gin.Context) {
	tenant, domain, ok := h.tenantDomain(c)
	if !ok {
		return
	}
	id, ok := dnsPlanID(c)
	if !ok {
		return
	}
	var input struct {
		Digest  string `json:"source_digest"`
		Confirm bool   `json:"confirm"`
	}
	if c.ShouldBindJSON(&input) != nil || !input.Confirm {
		authError(c, http.StatusBadRequest, "DNS_CONFIRMATION_REQUIRED")
		return
	}
	q := requestStore(c, h.Store)
	p, err := q.GetDNSPlan(c, store.GetDNSPlanParams{TenantID: tenant, ID: id})
	if err != nil || p.DomainID != domain {
		authError(c, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if err = q.ConfirmDNSPlan(c, store.ConfirmDNSPlanParams{TenantID: tenant, ID: id, SourceDigest: input.Digest}); err != nil {
		dnsError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "accepted"})
}

// Cancel does not pretend that an in-flight external write can be undone.
func (h *DNSPlanHandler) Cancel(c *gin.Context) {
	tenant, domain, ok := h.tenantDomain(c)
	if !ok {
		return
	}
	id, ok := dnsPlanID(c)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&input) != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	q := requestStore(c, h.Store)
	p, err := q.GetDNSPlan(c, store.GetDNSPlanParams{TenantID: tenant, ID: id})
	if err != nil || p.DomainID != domain {
		authError(c, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if err = q.CancelDNSPlan(c, store.CancelDNSPlanParams{TenantID: tenant, ID: id, Reason: input.Reason}); err != nil {
		dnsError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Retry requests a fresh durable provisioning attempt after explicit review.
func (h *DNSPlanHandler) Retry(c *gin.Context) {
	tenant, domain, ok := h.tenantDomain(c)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&input) != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if err := requestStore(c, h.Store).RequestOnboardingRetry(c, store.RequestOnboardingRetryParams{TenantID: tenant, DomainID: domain, Reason: input.Reason}); err != nil {
		dnsError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "accepted"})
}

// Reconcile accepts only a fresh read-only observation, with explicit reason.
func (h *DNSPlanHandler) Reconcile(c *gin.Context) {
	tenant, domain, ok := h.tenantDomain(c)
	if !ok {
		return
	}
	value, exists := c.Get(dnsPreparedKey)
	proof, ok := value.(preparedDNSObservation)
	if !exists || !ok || proof.Tenant != tenant || proof.Domain != domain {
		authError(c, http.StatusServiceUnavailable, "DNS_PREPARATION_REQUIRED")
		return
	}
	q := requestStore(c, h.Store)
	p, err := q.GetDNSPlan(c, store.GetDNSPlanParams{TenantID: tenant, ID: proof.ID})
	if err != nil || p.DomainID != domain || p.Status != "uncertain" || p.Fence != proof.Fence {
		authError(c, http.StatusConflict, "DNS_REVIEW_REQUIRED")
		return
	}
	if err = q.ReviewDNSPlan(c, store.ReviewDNSPlanParams{TenantID: tenant, ID: proof.ID, Reason: proof.Reason}); err != nil {
		dnsError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Rollback requests a durable guarded inverse; no handler writes DNS itself.
func (h *DNSPlanHandler) Rollback(c *gin.Context) {
	tenant, domain, ok := h.tenantDomain(c)
	if !ok {
		return
	}
	id, ok := dnsPlanID(c)
	if !ok {
		return
	}
	var input struct {
		Reason  string `json:"reason"`
		Confirm bool   `json:"confirm"`
	}
	if c.ShouldBindJSON(&input) != nil || !input.Confirm || len(strings.TrimSpace(input.Reason)) < 10 {
		authError(c, http.StatusBadRequest, "DNS_REVIEW_REQUIRED")
		return
	}
	q := requestStore(c, h.Store)
	p, err := q.GetDNSPlan(c, store.GetDNSPlanParams{TenantID: tenant, ID: id})
	if err != nil || p.DomainID != domain {
		authError(c, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if err = q.RequestDNSRollback(c, store.RequestDNSRollbackParams{TenantID: tenant, ID: id, Reason: input.Reason}); err != nil {
		dnsError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "accepted"})
}

// Abandon preserves uncertain journals and requires a new upstream preview;
// it does not report a rollback or silently replay abandoned writes.
func (h *DNSPlanHandler) Abandon(c *gin.Context) {
	tenant, domain, ok := h.tenantDomain(c)
	if !ok {
		return
	}
	id, ok := dnsPlanID(c)
	if !ok {
		return
	}
	var input struct {
		Reason  string `json:"reason"`
		Confirm bool   `json:"confirm_unresolved"`
	}
	if c.ShouldBindJSON(&input) != nil || !input.Confirm || len(strings.TrimSpace(input.Reason)) < 10 {
		authError(c, http.StatusBadRequest, "DNS_REVIEW_REQUIRED")
		return
	}
	q := requestStore(c, h.Store)
	p, err := q.GetDNSPlan(c, store.GetDNSPlanParams{TenantID: tenant, ID: id})
	if err != nil || p.DomainID != domain {
		authError(c, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if err = q.AbandonDNSPlan(c, store.AbandonDNSPlanParams{TenantID: tenant, ID: id, Reason: input.Reason}); err != nil {
		dnsError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
