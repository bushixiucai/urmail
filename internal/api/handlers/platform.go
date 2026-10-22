package handlers

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

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
}

type PlatformHandler struct {
	Store PlatformStore
	Pool  *db.Pool
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
