package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/store"
)

// PlatformStore is the platform-pool query surface used by the admin API.
type PlatformStore interface {
	PlatformOverview(ctx context.Context) (store.PlatformOverviewRow, error)
	PlatformListTenants(ctx context.Context) ([]store.PlatformListTenantsRow, error)
	PlatformUpdateTenantStatus(ctx context.Context, arg store.PlatformUpdateTenantStatusParams) (store.PlatformUpdateTenantStatusRow, error)
	PlatformListBrands(ctx context.Context) ([]store.PlatformListBrandsRow, error)
	PlatformUpsertBrand(ctx context.Context, arg store.PlatformUpsertBrandParams) (store.PlatformUpsertBrandRow, error)
	PlatformListChannels(ctx context.Context) ([]store.Channels, error)
	PlatformUpsertChannel(ctx context.Context, arg store.PlatformUpsertChannelParams) (store.Channels, error)
	PlatformListAuditLogs(ctx context.Context, limit int32) ([]store.AuditLogs, error)
}

// PlatformHandler serves operations authorized by the current platform role.
type PlatformHandler struct {
	Store     PlatformStore
	Pool      *db.Pool
	Reconcile ReconcileTrigger
}

// NewPlatformHandler constructs the platform administration handler.
func NewPlatformHandler(s PlatformStore) *PlatformHandler {
	return &PlatformHandler{Store: s}
}

// PlatformTenant exposes the tenant fields used by the platform console.
type PlatformTenant struct {
	ID           int64              `json:"id"`
	BrandID      int64              `json:"brand_id"`
	Name         string             `json:"name"`
	Slug         string             `json:"slug"`
	Status       string             `json:"status"`
	DomainCount  int64              `json:"domain_count"`
	MailboxCount int64              `json:"mailbox_count"`
	CreatedAt    pgtype.Timestamptz `json:"created_at"`
}

// Overview returns aggregate platform resource counts.
func (h *PlatformHandler) Overview(c *gin.Context) {
	row, err := requestStore(c, h.Store).PlatformOverview(c)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"overview": row})
}

// ListTenants lists tenants with resource counts.
func (h *PlatformHandler) ListTenants(c *gin.Context) {
	rows, err := requestStore(c, h.Store).PlatformListTenants(c)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	out := make([]PlatformTenant, 0, len(rows))
	for _, row := range rows {
		out = append(out, PlatformTenant{
			ID:           row.ID,
			BrandID:      row.BrandID,
			Name:         row.Name,
			Slug:         row.Slug,
			Status:       row.Status,
			DomainCount:  row.DomainCount,
			MailboxCount: row.MailboxCount,
			CreatedAt:    row.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"tenants": out})
}

// SuspendTenant suspends a tenant and schedules configuration application.
func (h *PlatformHandler) SuspendTenant(c *gin.Context) {
	h.setStatus(c, "suspended")
}

// ActivateTenant reactivates a tenant and schedules configuration application.
func (h *PlatformHandler) ActivateTenant(c *gin.Context) {
	h.setStatus(c, "active")
}

// CloseTenant closes a tenant without deleting historical billing data.
func (h *PlatformHandler) CloseTenant(c *gin.Context) {
	h.setStatus(c, "closed")
}

func (h *PlatformHandler) setStatus(c *gin.Context, status string) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	if lock, ok := requestStore(c, h.Store).(interface {
		PlatformLockBillingTenant(context.Context, int64) error
	}); ok {
		if err := lock.PlatformLockBillingTenant(c, id); err != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
	}
	row, err := requestStore(c, h.Store).PlatformUpdateTenantStatus(c, store.PlatformUpdateTenantStatusParams{
		ID:     id,
		Status: status,
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "TENANT_NOT_FOUND"}})
		return
	}
	if h.Reconcile != nil {
		middleware.AfterCommit(c, h.Reconcile.Trigger)
	}
	c.JSON(http.StatusOK, gin.H{"tenant": PlatformTenant{
		ID:        row.ID,
		BrandID:   row.BrandID,
		Name:      row.Name,
		Slug:      row.Slug,
		Status:    row.Status,
		CreatedAt: row.CreatedAt,
	}})
}

// Brands lists the compatibility brand administration view.
func (h *PlatformHandler) Brands(c *gin.Context) {
	rows, err := requestStore(c, h.Store).PlatformListBrands(c)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"brands": rows})
}

var tenantSlugPattern = regexp.MustCompile("^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$")

// CreateTenantRequest identifies a tenant and its initial owner.
type CreateTenantRequest struct {
	Name          string `json:"name" binding:"required"`
	Slug          string `json:"slug" binding:"required"`
	BrandSlug     string `json:"brand_slug"`
	OwnerEmail    string `json:"owner_email" binding:"required"`
	OwnerPassword string `json:"owner_password" binding:"required,min=12,max=256"`
	FreeUntil     string `json:"free_until"`
	FreeReason    string `json:"free_reason"`
}

// CreateTenant creates a brand (if needed), tenant and owner account in one
// platform transaction. The owner is not made a platform administrator.
func (h *PlatformHandler) CreateTenant(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	var req CreateTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	name := strings.TrimSpace(req.Name)
	slug := strings.ToLower(strings.TrimSpace(req.Slug))
	brandSlug := strings.ToLower(strings.TrimSpace(req.BrandSlug))
	if brandSlug == "" {
		brandSlug = "default"
	}
	owner := strings.ToLower(strings.TrimSpace(req.OwnerEmail))
	if name == "" || !tenantSlugPattern.MatchString(slug) || !tenantSlugPattern.MatchString(brandSlug) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	if !strings.Contains(owner, "@") {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	status := "pending"
	freeEnd := pgtype.Timestamptz{}
	if req.FreeUntil != "" {
		until, e := time.Parse(time.RFC3339, req.FreeUntil)
		if e != nil || !until.After(time.Now()) || strings.TrimSpace(req.FreeReason) == "" || len(req.FreeReason) > 2000 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		status = "active"
		freeEnd = pgtype.Timestamptz{Time: until.UTC(), Valid: true}
	} else if strings.TrimSpace(req.FreeReason) != "" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	hash, err := panelpassword.HashContext(c.Request.Context(), req.OwnerPassword)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "HASH_FAILED"}})
		return
	}

	q, finish, rollback, err := platformMutationQueries(c, h.Pool)
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	defer rollback()
	brandID, err := q.PlatformEnsureManagedBrand(c, store.PlatformEnsureManagedBrandParams{Slug: brandSlug, Name: req.Name})
	if supportError(c, err) {
		return
	}
	tenantID, err := q.PlatformCreateManagedTenant(c, store.PlatformCreateManagedTenantParams{BrandID: brandID, Name: name, Slug: slug, Status: status})
	if err != nil {
		if !respondConstraintError(c, err) {
			authError(c, 409, "TENANT_EXISTS")
		}
		return
	}
	userID, err := q.PlatformFindManagedUser(c, owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		userID, err = q.PlatformCreateManagedUser(c, store.PlatformCreateManagedUserParams{Email: owner, PasswordHash: hash, DisplayName: pgtype.Text{String: name, Valid: true}})
	case err != nil:
	}
	if supportError(c, err) {
		return
	}
	if supportError(c, q.PlatformGrantTenantOwner(c, store.PlatformGrantTenantOwnerParams{UserID: userID, TenantID: tenantID})) {
		return
	}
	if _, err = q.PlatformSetTenantOwner(c, store.PlatformSetTenantOwnerParams{ID: tenantID, OwnerUserID: pgtype.Int8{Int64: userID, Valid: true}}); supportError(c, err) {
		return
	}
	if freeEnd.Valid {
		if supportError(c, q.PlatformSetManagedFreeTerm(c, store.PlatformSetManagedFreeTermParams{TenantID: tenantID, PeriodEnd: freeEnd})) {
			return
		}
		middleware.SetPlatformAuditDetail(c, map[string]any{"free_until": freeEnd.Time.Format(time.RFC3339), "grant_reason": strings.TrimSpace(req.FreeReason)})
	} else if supportError(c, q.PlatformRemoveRegistrationGift(c, tenantID)) {
		return
	}
	middleware.SetPlatformAuditTenant(c, tenantID)
	if supportError(c, finish()) {
		return
	}
	c.JSON(http.StatusCreated, gin.H{"tenant": PlatformTenant{
		ID: tenantID, BrandID: brandID, Name: name, Slug: slug, Status: status,
	}})
}

// BrandInput defines editable visual branding fields.
type BrandInput struct {
	Slug         string          `json:"slug" binding:"required"`
	Name         string          `json:"name" binding:"required"`
	Theme        json.RawMessage `json:"theme"`
	LogoURL      string          `json:"logo_url"`
	FaviconURL   string          `json:"favicon_url"`
	PrimaryColor string          `json:"primary_color"`
}

// UpsertBrand serves the compatibility brand upsert implementation.
func (h *PlatformHandler) UpsertBrand(c *gin.Context) {
	var in BrandInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	theme := in.Theme
	if len(theme) == 0 {
		theme = json.RawMessage("{}")
	}
	if !json.Valid(theme) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	brand, err := requestStore(c, h.Store).PlatformUpsertBrand(c, store.PlatformUpsertBrandParams{
		Slug: in.Slug, Name: in.Name, Theme: theme,
		LogoUrl: textOrNull(in.LogoURL), FaviconUrl: textOrNull(in.FaviconURL),
		PrimaryColor: textOrNull(in.PrimaryColor),
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"brand": brand})
}

func textOrNull(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

// ChannelInput defines a distribution channel and commission settings.
type ChannelInput struct {
	Code          string          `json:"code" binding:"required"`
	Name          string          `json:"name" binding:"required"`
	Kind          string          `json:"kind"`
	CommissionBps int32           `json:"commission_bps"`
	BrandID       *int64          `json:"brand_id"`
	ParentID      *int64          `json:"parent_id"`
	Contact       json.RawMessage `json:"contact"`
	Active        *bool           `json:"active"`
}

// Channels lists platform-managed distribution channels.
func (h *PlatformHandler) Channels(c *gin.Context) {
	rows, err := requestStore(c, h.Store).PlatformListChannels(c)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"channels": rows})
}

// UpsertChannel validates and saves a distribution channel.
func (h *PlatformHandler) UpsertChannel(c *gin.Context) {
	var in ChannelInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	kind := in.Kind
	if kind == "" {
		kind = "agent"
	}
	if kind != "self" && kind != "agent" && kind != "reseller" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	if in.CommissionBps < 0 || in.CommissionBps > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	contact := in.Contact
	if len(contact) == 0 {
		contact = json.RawMessage("{}")
	}
	if !json.Valid(contact) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	var brandID, parentID pgtype.Int8
	if in.BrandID != nil {
		brandID = pgtype.Int8{Int64: *in.BrandID, Valid: true}
	}
	if in.ParentID != nil {
		parentID = pgtype.Int8{Int64: *in.ParentID, Valid: true}
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	row, err := requestStore(c, h.Store).PlatformUpsertChannel(c, store.PlatformUpsertChannelParams{
		BrandID: brandID, Code: in.Code, Name: in.Name, Kind: kind,
		CommissionBps: in.CommissionBps, ParentID: parentID,
		Contact: contact, Active: active,
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"channel": row})
}

// UpdateTenantRequest supplies the tenant display name.
type UpdateTenantRequest struct {
	Name string `json:"name" binding:"required"`
}

// UpdateTenant changes a tenant display name.
func (h *PlatformHandler) UpdateTenant(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}})
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
	var req UpdateTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	if _, err := requestStore(c, store.New(h.Pool.Pool)).PlatformRenameTenant(c, store.PlatformRenameTenantParams{ID: id, Name: strings.TrimSpace(req.Name)}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// TransferOwnerRequest identifies an existing account as the new tenant owner.
type TransferOwnerRequest struct {
	Email string `json:"email" binding:"required"`
}

// TransferOwner atomically transfers tenant ownership.
func (h *PlatformHandler) TransferOwner(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}})
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
	var req TransferOwnerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	q, finish, rollback, err := platformMutationQueries(c, h.Pool)
	if supportError(c, err) {
		return
	}
	defer rollback()
	if supportError(c, q.PlatformLockBillingTenant(c, id)) {
		return
	}
	userID, err := q.PlatformFindManagedUser(c, email)
	if err != nil {
		authError(c, 404, "USER_NOT_FOUND")
		return
	}
	if supportError(c, q.PlatformDemotePreviousOwners(c, store.PlatformDemotePreviousOwnersParams{TenantID: id, UserID: userID})) {
		return
	}
	if supportError(c, q.PlatformGrantTenantOwner(c, store.PlatformGrantTenantOwnerParams{UserID: userID, TenantID: id})) {
		return
	}
	if _, err = q.PlatformSetTenantOwner(c, store.PlatformSetTenantOwnerParams{ID: id, OwnerUserID: pgtype.Int8{Int64: userID, Valid: true}}); supportError(c, err) {
		return
	}
	if supportError(c, finish()) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "owner_user_id": userID})
}

// AuditLogs lists redacted platform audit events.
func (h *PlatformHandler) AuditLogs(c *gin.Context) {
	limit := int32(100)
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 32); err == nil && v > 0 && v <= 500 {
			limit = int32(v)
		}
	}
	logs, err := requestStore(c, h.Store).PlatformListAuditLogs(c, limit)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs": auditResponses(logs)})
}
