package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

// PlatformStore is the platform-pool query surface used by the admin API.
type PlatformStore interface {
	PlatformOverview(ctx context.Context) (store.PlatformOverviewRow, error)
	PlatformListTenants(ctx context.Context) ([]store.PlatformListTenantsRow, error)
	PlatformUpdateTenantStatus(ctx context.Context, arg store.PlatformUpdateTenantStatusParams) (store.PlatformUpdateTenantStatusRow, error)
	PlatformListBrands(ctx context.Context) ([]store.PlatformListBrandsRow, error)
	PlatformListPlans(ctx context.Context) ([]store.Plans, error)
	PlatformListCoupons(ctx context.Context) ([]store.Coupons, error)
	PlatformUpsertPlan(ctx context.Context, arg store.PlatformUpsertPlanParams) (store.Plans, error)
	PlatformUpsertCoupon(ctx context.Context, arg store.PlatformUpsertCouponParams) (store.Coupons, error)
	PlatformUpsertBrand(ctx context.Context, arg store.PlatformUpsertBrandParams) (store.PlatformUpsertBrandRow, error)
	PlatformListChannels(ctx context.Context) ([]store.Channels, error)
	PlatformUpsertChannel(ctx context.Context, arg store.PlatformUpsertChannelParams) (store.Channels, error)
	PlatformListAuditLogs(ctx context.Context, limit int32) ([]store.AuditLogs, error)
}

type PlatformHandler struct {
	Store     PlatformStore
	Pool      *db.Pool
	Reconcile ReconcileTrigger
}

func NewPlatformHandler(s PlatformStore) *PlatformHandler {
	return &PlatformHandler{Store: s}
}

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

func (h *PlatformHandler) Overview(c *gin.Context) {
	row, err := h.Store.PlatformOverview(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"overview": row})
}

func (h *PlatformHandler) ListTenants(c *gin.Context) {
	rows, err := h.Store.PlatformListTenants(c)
	if err != nil {
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

func (h *PlatformHandler) SuspendTenant(c *gin.Context) {
	h.setStatus(c, "suspended")
}

func (h *PlatformHandler) ActivateTenant(c *gin.Context) {
	h.setStatus(c, "active")
}

func (h *PlatformHandler) CloseTenant(c *gin.Context) {
	h.setStatus(c, "closed")
}

func (h *PlatformHandler) setStatus(c *gin.Context, status string) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	row, err := h.Store.PlatformUpdateTenantStatus(c, store.PlatformUpdateTenantStatusParams{
		ID:     id,
		Status: status,
	})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "TENANT_NOT_FOUND"}})
		return
	}
	if h.Reconcile != nil {
		h.Reconcile.Trigger()
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

func (h *PlatformHandler) Brands(c *gin.Context) {
	rows, err := h.Store.PlatformListBrands(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"brands": rows})
}

func (h *PlatformHandler) Plans(c *gin.Context) {
	rows, err := h.Store.PlatformListPlans(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plans": rows})
}

func (h *PlatformHandler) Coupons(c *gin.Context) {
	rows, err := h.Store.PlatformListCoupons(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"coupons": rows})
}

var tenantSlugPattern = regexp.MustCompile("^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$")

type CreateTenantRequest struct {
	Name          string `json:"name" binding:"required"`
	Slug          string `json:"slug" binding:"required"`
	BrandSlug     string `json:"brand_slug"`
	OwnerEmail    string `json:"owner_email" binding:"required"`
	OwnerPassword string `json:"owner_password" binding:"required,min=12,max=256"`
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
	hash, err := domainMail.HashPassword(req.OwnerPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "HASH_FAILED"}})
		return
	}

	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	defer func() { _ = tx.Rollback(c) }()

	var brandID int64
	if err := tx.QueryRow(c, `
		INSERT INTO brands (slug, name)
		VALUES ($1, $2)
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`, brandSlug, req.Name).Scan(&brandID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	var tenantID int64
	err = tx.QueryRow(c, `
		INSERT INTO tenants (brand_id, name, slug, status)
		VALUES ($1, $2, $3, 'active')
		RETURNING id`, brandID, name, slug).Scan(&tenantID)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "TENANT_EXISTS"}})
		return
	}
	var userID int64
	err = tx.QueryRow(c, "SELECT id FROM users WHERE email = $1", owner).Scan(&userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(c, `
			INSERT INTO users (email, password_hash, display_name, status)
			VALUES ($1, $2, $3, 'active')
			RETURNING id`, owner, hash, name).Scan(&userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
			return
		}
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if _, err := tx.Exec(c, `
		INSERT INTO user_tenant_roles (user_id, tenant_id, role)
		VALUES ($1, $2, 'owner')
		ON CONFLICT (user_id, tenant_id) DO UPDATE SET role = 'owner'`, userID, tenantID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if _, err := tx.Exec(c, `UPDATE tenants SET owner_user_id = $2 WHERE id = $1`, tenantID, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if err := tx.Commit(c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"tenant": PlatformTenant{
		ID: tenantID, BrandID: brandID, Name: name, Slug: slug, Status: "active",
	}})
}

type PlanInput struct {
	Code           string          `json:"code" binding:"required"`
	Name           json.RawMessage `json:"name"`
	Description    json.RawMessage `json:"description"`
	PriceCents     int64           `json:"price_cents"`
	Currency       string          `json:"currency"`
	QuotaBytes     int64           `json:"quota_bytes"`
	MailboxLimit   int32           `json:"mailbox_limit"`
	DomainLimit    int32           `json:"domain_limit"`
	DailySendLimit int32           `json:"daily_send_limit"`
	Features       json.RawMessage `json:"features"`
	SortOrder      int32           `json:"sort_order"`
	Active         *bool           `json:"active"`
}

func (h *PlatformHandler) UpsertPlan(c *gin.Context) {
	var in PlanInput
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Code) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	name := in.Name
	if len(name) == 0 {
		name = json.RawMessage(`{"en":"` + in.Code + `"}`)
	}
	description := in.Description
	if len(description) == 0 {
		description = json.RawMessage("{}")
	}
	features := in.Features
	if len(features) == 0 {
		features = json.RawMessage("[]")
	}
	if !json.Valid(name) || !json.Valid(description) || !json.Valid(features) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	currency := in.Currency
	if currency == "" {
		currency = "CNY"
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	plan, err := h.Store.PlatformUpsertPlan(c, store.PlatformUpsertPlanParams{
		Code: in.Code, Name: name, Description: description,
		PriceCents: in.PriceCents, Currency: currency, QuotaBytes: in.QuotaBytes,
		MailboxLimit: in.MailboxLimit, DomainLimit: in.DomainLimit,
		DailySendLimit: in.DailySendLimit, Features: features,
		SortOrder: in.SortOrder, Active: active,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plan": plan})
}

type CouponInput struct {
	Code           string `json:"code" binding:"required"`
	Kind           string `json:"kind" binding:"required"`
	Value          int64  `json:"value"`
	MaxRedemptions *int32 `json:"max_redemptions"`
	ValidFrom      string `json:"valid_from"`
	ValidTo        string `json:"valid_to"`
	Active         *bool  `json:"active"`
}

func (h *PlatformHandler) UpsertCoupon(c *gin.Context) {
	var in CouponInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	if in.Kind != "percent" && in.Kind != "fixed" && in.Kind != "trial_days" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	var from, to pgtype.Timestamptz
	if in.ValidFrom != "" {
		t, err := time.Parse(time.RFC3339, in.ValidFrom)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
			return
		}
		from = pgtype.Timestamptz{Time: t, Valid: true}
	}
	if in.ValidTo != "" {
		t, err := time.Parse(time.RFC3339, in.ValidTo)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
			return
		}
		to = pgtype.Timestamptz{Time: t, Valid: true}
	}
	max := pgtype.Int4{}
	if in.MaxRedemptions != nil {
		max = pgtype.Int4{Int32: *in.MaxRedemptions, Valid: true}
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	coupon, err := h.Store.PlatformUpsertCoupon(c, store.PlatformUpsertCouponParams{
		Code: in.Code, Kind: in.Kind, Value: in.Value, MaxRedemptions: max,
		ValidFrom: from, ValidTo: to, Active: active,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"coupon": coupon})
}

type BrandInput struct {
	Slug         string          `json:"slug" binding:"required"`
	Name         string          `json:"name" binding:"required"`
	Theme        json.RawMessage `json:"theme"`
	LogoURL      string          `json:"logo_url"`
	FaviconURL   string          `json:"favicon_url"`
	PrimaryColor string          `json:"primary_color"`
}

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
	brand, err := h.Store.PlatformUpsertBrand(c, store.PlatformUpsertBrandParams{
		Slug: in.Slug, Name: in.Name, Theme: theme,
		LogoUrl: textOrNull(in.LogoURL), FaviconUrl: textOrNull(in.FaviconURL),
		PrimaryColor: textOrNull(in.PrimaryColor),
	})
	if err != nil {
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

func (h *PlatformHandler) Channels(c *gin.Context) {
	rows, err := h.Store.PlatformListChannels(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"channels": rows})
}

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
	row, err := h.Store.PlatformUpsertChannel(c, store.PlatformUpsertChannelParams{
		BrandID: brandID, Code: in.Code, Name: in.Name, Kind: kind,
		CommissionBps: in.CommissionBps, ParentID: parentID,
		Contact: contact, Active: active,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"channel": row})
}

type UpdateTenantRequest struct {
	Name string `json:"name" binding:"required"`
}

func (h *PlatformHandler) UpdateTenant(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	var req UpdateTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	if _, err := h.Pool.Exec(c, "UPDATE tenants SET name = $2 WHERE id = $1", id, strings.TrimSpace(req.Name)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

type TransferOwnerRequest struct {
	Email string `json:"email" binding:"required"`
}

func (h *PlatformHandler) TransferOwner(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	var req TransferOwnerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	defer func() { _ = tx.Rollback(c) }()
	var userID int64
	if err := tx.QueryRow(c, "SELECT id FROM users WHERE email = $1", email).Scan(&userID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "USER_NOT_FOUND"}})
		return
	}
	if _, err := tx.Exec(c, `
		UPDATE user_tenant_roles SET role = 'admin'
		WHERE tenant_id = $1 AND role = 'owner' AND user_id <> $2`, id, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if _, err := tx.Exec(c, `
		INSERT INTO user_tenant_roles (user_id, tenant_id, role)
		VALUES ($1, $2, 'owner')
		ON CONFLICT (user_id, tenant_id) DO UPDATE SET role = 'owner'`, userID, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if _, err := tx.Exec(c, "UPDATE tenants SET owner_user_id = $2 WHERE id = $1", id, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if err := tx.Commit(c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "owner_user_id": userID})
}

func (h *PlatformHandler) AuditLogs(c *gin.Context) {
	limit := int32(100)
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 500 {
			limit = int32(v)
		}
	}
	logs, err := h.Store.PlatformListAuditLogs(c, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs": logs})
}

type AssignSubscriptionRequest struct {
	PlanCode  string `json:"plan_code" binding:"required"`
	PeriodEnd string `json:"period_end"`
}

// AssignSubscription replaces the tenant's active subscription. Payment and
// invoicing remain separate steps; this is the administrative entitlement
// change used by the platform console and support tooling.
func (h *PlatformHandler) AssignSubscription(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	var req AssignSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.PlanCode) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	periodEnd := time.Now().AddDate(0, 1, 0)
	if req.PeriodEnd != "" {
		t, err := time.Parse(time.RFC3339, req.PeriodEnd)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
			return
		}
		periodEnd = t
	}
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	defer func() { _ = tx.Rollback(c) }()
	var exists bool
	if err := tx.QueryRow(c, "SELECT EXISTS (SELECT 1 FROM plans WHERE code = $1 AND active)", req.PlanCode).Scan(&exists); err != nil || !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "PLAN_NOT_FOUND"}})
		return
	}
	if _, err := tx.Exec(c, `
		UPDATE subscriptions SET status = 'canceled'
		WHERE tenant_id = $1 AND status IN ('active', 'trialing', 'past_due')`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if _, err := tx.Exec(c, `
		INSERT INTO subscriptions (tenant_id, plan_id, status, period_start, period_end)
		VALUES ($1, $2, 'active', now(), $3)`, id, req.PlanCode, periodEnd); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if err := tx.Commit(c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "plan_code": req.PlanCode, "period_end": periodEnd})
}

type RedeemCouponRequest struct {
	TenantID int64  `json:"tenant_id" binding:"required"`
	OrderID  *int64 `json:"order_id"`
}

func (h *PlatformHandler) RedeemCoupon(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	var req RedeemCouponRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.TenantID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	defer func() { _ = tx.Rollback(c) }()

	var max pgtype.Int4
	var count int32
	if err := tx.QueryRow(c, `
		SELECT max_redemptions, redeemed_count FROM coupons
		WHERE code = $1 AND active = true
		  AND (valid_to IS NULL OR valid_to > now())
		  AND (valid_from IS NULL OR valid_from <= now())
		FOR UPDATE`, c.Param("code")).Scan(&max, &count); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "COUPON_NOT_FOUND"}})
		return
	}
	var exists bool
	if err := tx.QueryRow(c, "SELECT EXISTS (SELECT 1 FROM coupon_redemptions WHERE coupon_code = $1 AND tenant_id = $2)", c.Param("code"), req.TenantID).Scan(&exists); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if exists {
		c.JSON(http.StatusOK, gin.H{"status": "already_redeemed"})
		return
	}
	if max.Valid && count >= max.Int32 {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "COUPON_EXHAUSTED"}})
		return
	}
	var order pgtype.Int8
	if req.OrderID != nil {
		order = pgtype.Int8{Int64: *req.OrderID, Valid: true}
	}
	if _, err := tx.Exec(c, "INSERT INTO coupon_redemptions (coupon_code, tenant_id, order_id) VALUES ($1, $2, $3)", c.Param("code"), req.TenantID, order); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "COUPON_CONFLICT"}})
		return
	}
	if _, err := tx.Exec(c, "UPDATE coupons SET redeemed_count = redeemed_count + 1 WHERE code = $1", c.Param("code")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if err := tx.Commit(c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "redeemed"})
}

type InvoiceInput struct {
	Number      string `json:"number"`
	AmountCents int64  `json:"amount_cents" binding:"required"`
	Currency    string `json:"currency"`
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
}

func parseTime(value string) (pgtype.Timestamptz, error) {
	if value == "" {
		return pgtype.Timestamptz{}, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return pgtype.Timestamptz{}, err
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, nil
}

func (h *PlatformHandler) CreateInvoice(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	tenantID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	var in InvoiceInput
	if err := c.ShouldBindJSON(&in); err != nil || in.AmountCents <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	start, err := parseTime(in.PeriodStart)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	end, err := parseTime(in.PeriodEnd)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	if !start.Valid {
		start = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	if !end.Valid {
		end = pgtype.Timestamptz{Time: time.Now().AddDate(0, 1, 0), Valid: true}
	}
	number := strings.TrimSpace(in.Number)
	if number == "" {
		number = fmt.Sprintf("INV-%d-%d", tenantID, time.Now().Unix())
	}
	currency := in.Currency
	if currency == "" {
		currency = "CNY"
	}
	var id int64
	err = h.Pool.QueryRow(c, `
		INSERT INTO invoices (tenant_id, number, period_start, period_end, amount_cents, currency, status, issued_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'issued', now())
		RETURNING id`, tenantID, number, start, end, in.AmountCents, currency).Scan(&id)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "INVOICE_EXISTS"}})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "number": number})
}

func (h *PlatformHandler) MarkInvoicePaid(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	tag, err := h.Pool.Exec(c, "UPDATE invoices SET status = 'paid' WHERE id = $1 AND status IN ('draft','issued')", id)
	if err != nil || tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "INVOICE_NOT_FOUND"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "paid"})
}

func (h *PlatformHandler) VoidInvoice(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	tag, err := h.Pool.Exec(c, "UPDATE invoices SET status = 'void' WHERE id = $1 AND status IN ('draft','issued')", id)
	if err != nil || tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "INVOICE_NOT_FOUND"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "void"})
}

func (h *PlatformHandler) MarkOrderPaid(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	defer func() { _ = tx.Rollback(c) }()
	var tenantID, amount int64
	var planCode, currency string
	err = tx.QueryRow(c, `
		SELECT tenant_id, amount_cents, COALESCE(plan_code, ''), currency
		FROM orders WHERE id = $1 AND status = 'pending' FOR UPDATE`, id).
		Scan(&tenantID, &amount, &planCode, &currency)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "ORDER_NOT_FOUND"}})
		return
	}
	if _, err := tx.Exec(c, "UPDATE orders SET status = 'paid', paid_at = now() WHERE id = $1", id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if planCode != "" {
		if _, err := tx.Exec(c, `
			UPDATE subscriptions SET status = 'canceled'
			WHERE tenant_id = $1 AND status IN ('active', 'trialing', 'past_due')`, tenantID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
			return
		}
		if _, err := tx.Exec(c, `
			INSERT INTO subscriptions (tenant_id, plan_id, status, period_start, period_end)
			VALUES ($1, $2, 'active', now(), now() + interval '1 month')`, tenantID, planCode); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
			return
		}
	}
	if _, err := tx.Exec(c, `
		INSERT INTO invoices (tenant_id, number, period_start, period_end, amount_cents, currency, status, issued_at)
		VALUES ($1, $2, now(), now() + interval '1 month', $3, $4, 'paid', now())`,
		tenantID, fmt.Sprintf("INV-%d-%d", tenantID, time.Now().UnixNano()), amount, currency); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if err := tx.Commit(c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "paid"})
}
