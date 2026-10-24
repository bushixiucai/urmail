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

	"github.com/bushixiucai/urmail/internal/api/middleware"
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
	PlatformRedeemCoupon(ctx context.Context, arg store.PlatformRedeemCouponParams) (string, error)
	PlatformUpsertPlan(ctx context.Context, arg store.PlatformUpsertPlanParams) (store.Plans, error)
	PlatformUpsertCoupon(ctx context.Context, arg store.PlatformUpsertCouponParams) (store.Coupons, error)
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

// Plans lists active and inactive plans with structured JSON metadata.
func (h *PlatformHandler) Plans(c *gin.Context) {
	rows, err := requestStore(c, h.Store).PlatformListPlans(c)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plans": planResponses(rows)})
}

// Coupons lists platform-managed coupons.
func (h *PlatformHandler) Coupons(c *gin.Context) {
	rows, err := requestStore(c, h.Store).PlatformListCoupons(c)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"coupons": rows})
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
	hash, err := domainMail.HashPassword(req.OwnerPassword)
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

// PlanInput defines editable plan pricing, limits and availability.
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

// UpsertPlan validates and saves a plan while database guards protect allocations.
func (h *PlatformHandler) UpsertPlan(c *gin.Context) {
	var in PlanInput
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Code) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	if in.PriceCents < 0 || in.PriceCents > 9007199254740991 || in.QuotaBytes < -1 || in.MailboxLimit < -1 || in.DomainLimit < -1 || in.DailySendLimit < -1 {
		c.JSON(400, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
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
		features = json.RawMessage("{}")
	}
	if !json.Valid(name) || !json.Valid(description) || !json.Valid(features) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if currency == "" {
		currency = "CNY"
	}
	if currency != "CNY" && currency != "USD" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	queries := requestStore(c, h.Store)
	var finish func() error
	if _, scoped := middleware.QueriesFromCtx(c); !scoped && h.Pool != nil {
		tx, beginErr := h.Pool.PlatformTx(c.Request.Context())
		if beginErr != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		queries = store.New(tx)
		finish = func() error { return tx.Commit(c.Request.Context()) }
	}
	plan, err := queries.PlatformUpsertPlan(c, store.PlatformUpsertPlanParams{
		Code: in.Code, Name: name, Description: description,
		PriceCents: in.PriceCents, Currency: currency, QuotaBytes: in.QuotaBytes,
		MailboxLimit: in.MailboxLimit, DomainLimit: in.DomainLimit,
		DailySendLimit: in.DailySendLimit, Features: features,
		SortOrder: in.SortOrder, Active: active,
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if priceStore, ok := queries.(interface {
		PlatformSetPlanPrice(context.Context, store.PlatformSetPlanPriceParams) (store.PlatformSetPlanPriceRow, error)
	}); ok {
		if _, priceErr := priceStore.PlatformSetPlanPrice(c, store.PlatformSetPlanPriceParams{PlanCode: plan.Code, Currency: plan.Currency, BillingCycle: "monthly", AmountCents: plan.PriceCents}); priceErr != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
	}
	if finish != nil {
		if commitErr := finish(); commitErr != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"plan": newPlanResponse(plan)})
}

// CouponInput defines an editable coupon and its redemption limits.
type CouponInput struct {
	Currency       string `json:"currency"`
	Code           string `json:"code" binding:"required"`
	Kind           string `json:"kind" binding:"required"`
	Value          int64  `json:"value"`
	MaxRedemptions *int32 `json:"max_redemptions"`
	ValidFrom      string `json:"valid_from"`
	ValidTo        string `json:"valid_to"`
	Active         *bool  `json:"active"`
}

// UpsertCoupon validates and saves a coupon.
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
	if in.Value <= 0 || (in.Kind == "percent" && in.Value > 100) || (in.Kind == "trial_days" && in.Value > 365) || (in.Kind == "fixed" && in.Currency != "CNY" && in.Currency != "USD") {
		authError(c, 400, "INVALID_PARAMS")
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
	redemptionLimit := pgtype.Int4{}
	if in.MaxRedemptions != nil {
		redemptionLimit = pgtype.Int4{Int32: *in.MaxRedemptions, Valid: true}
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	coupon, err := requestStore(c, h.Store).PlatformUpsertCoupon(c, store.PlatformUpsertCouponParams{
		Code: in.Code, Kind: in.Kind, Value: in.Value, MaxRedemptions: redemptionLimit,
		Currency:  pgtype.Text{String: in.Currency, Valid: in.Currency != ""},
		ValidFrom: from, ValidTo: to, Active: active,
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"coupon": coupon})
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

// AssignSubscriptionRequest selects a plan and subscription period.
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
		if respondConstraintError(c, err) {
			return
		}
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
	if !periodEnd.After(time.Now()) {
		c.JSON(400, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	q, finish, rollback, err := platformMutationQueries(c, h.Pool)
	if supportError(c, err) {
		return
	}
	defer rollback()
	if supportError(c, q.PlatformLockBillingTenant(c, id)) {
		return
	}
	available, err := q.PlatformManagedPlanAvailable(c, req.PlanCode)
	if err != nil || !available {
		authError(c, 404, "PLAN_NOT_FOUND")
		return
	}
	if _, err = q.PlatformCancelActiveSubscriptions(c, id); supportError(c, err) {
		return
	}
	if supportError(c, q.PlatformGrantManagedSubscription(c, store.PlatformGrantManagedSubscriptionParams{TenantID: id, PlanID: req.PlanCode, PeriodEnd: pgtype.Timestamptz{Time: periodEnd, Valid: true}})) {
		return
	}
	if supportError(c, finish()) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "plan_code": req.PlanCode, "period_end": periodEnd})
}

// RedeemCouponRequest identifies a tenant for coupon redemption.
type RedeemCouponRequest struct {
	TenantID int64 `json:"tenant_id" binding:"required"`
	OrderID  int64 `json:"order_id" binding:"required,gt=0"`
}

// RedeemCoupon atomically records a permitted coupon redemption.
func (h *PlatformHandler) RedeemCoupon(c *gin.Context) {
	var req RedeemCouponRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.TenantID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	order := pgtype.Int8{Int64: req.OrderID, Valid: true}
	result, err := requestStore(c, h.Store).PlatformRedeemCoupon(c, store.PlatformRedeemCouponParams{Code: c.Param("code"), TenantID: req.TenantID, OrderID: order})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	switch result {
	case "redeemed":
		c.JSON(http.StatusOK, gin.H{"status": "redeemed"})
	case "already_redeemed":
		c.JSON(http.StatusOK, gin.H{"status": "already_redeemed"})
	case "exhausted":
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "COUPON_EXHAUSTED"}})
	default:
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "COUPON_NOT_FOUND"}})
	}
}

// InvoiceInput defines the invoice period, amount and currency.
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

// CreateInvoice issues an invoice for the specified tenant.
func (h *PlatformHandler) CreateInvoice(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	tenantID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
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
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	end, err := parseTime(in.PeriodEnd)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
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
	q, finish, rollback, err := platformMutationQueries(c, h.Pool)
	if supportError(c, err) {
		return
	}
	defer rollback()
	id, err := q.PlatformCreateManagedInvoice(c, store.PlatformCreateManagedInvoiceParams{TenantID: tenantID, Number: number, PeriodStart: start, PeriodEnd: end, AmountCents: in.AmountCents, Currency: currency})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "INVOICE_EXISTS"}})
		return
	}
	snapshot, err := CaptureInvoiceDocumentSnapshot(c, q, tenantID, in.AmountCents, currency, "observed_at_issuance")
	if supportError(c, err) {
		return
	}
	frozen, err := q.PlatformFreezeInvoiceDocumentSnapshot(c, store.PlatformFreezeInvoiceDocumentSnapshotParams{TenantID: tenantID, ID: id, DocumentSnapshot: snapshot})
	if supportError(c, err) {
		return
	}
	if frozen != 1 {
		authError(c, 500, "DB_ERROR")
		return
	}
	if supportError(c, finish()) {
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "number": number})
}

// MarkInvoicePaid marks an existing invoice paid.
func (h *PlatformHandler) MarkInvoicePaid(c *gin.Context) {
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
	affected, err := requestStore(c, store.New(h.Pool.Pool)).PlatformMarkManagedInvoicePaid(c, id)
	if err != nil || affected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "INVOICE_NOT_FOUND"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "paid"})
}

// VoidInvoice voids an existing invoice.
func (h *PlatformHandler) VoidInvoice(c *gin.Context) {
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
	affected, err := requestStore(c, store.New(h.Pool.Pool)).PlatformVoidManagedInvoice(c, id)
	if err != nil || affected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "INVOICE_NOT_FOUND"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "void"})
}

// MarkOrderPaid idempotently settles a pending or already-paid order.
func (h *PlatformHandler) MarkOrderPaid(c *gin.Context) {
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
	uid, _ := middleware.GetUserID(c)
	if err := SettleOrderWithDetails(c, h.Pool, id, SettleOptions{Provider: "manual", ActorUserID: uid, AuditAction: "APPROVE order.manual_payment"}); err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "ORDER_NOT_FOUND"}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "paid"})
}
