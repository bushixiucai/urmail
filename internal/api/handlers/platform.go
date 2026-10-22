package handlers

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

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
