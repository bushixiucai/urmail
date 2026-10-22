package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

type BillingStore interface {
	ListActivePlans(ctx context.Context) ([]store.Plans, error)
	GetPlanByCode(ctx context.Context, code string) (store.Plans, error)
	GetActiveSubscriptionByTenant(ctx context.Context, tenantID int64) (store.Subscriptions, error)
	ListInvoicesByTenant(ctx context.Context, tenantID int64) ([]store.Invoices, error)
	GetCouponByCode(ctx context.Context, code string) (store.Coupons, error)
	GetChannelAttributionByTenant(ctx context.Context, tenantID int64) (store.ChannelAttribution, error)
}

type BillingHandler struct {
	Store BillingStore
}

func NewBillingHandler(s BillingStore) *BillingHandler {
	return &BillingHandler{Store: s}
}

func (h *BillingHandler) Plans(c *gin.Context) {
	plans, err := h.Store.ListActivePlans(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plans": plans})
}

func (h *BillingHandler) Subscription(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	sub, err := h.Store.GetActiveSubscriptionByTenant(c, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusOK, gin.H{"subscription": nil, "plan": nil})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	plan, err := h.Store.GetPlanByCode(c, sub.PlanID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"subscription": sub, "plan": plan})
}

func (h *BillingHandler) Invoices(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	invoices, err := h.Store.ListInvoicesByTenant(c, tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"invoices": invoices})
}

type validateCouponRequest struct {
	Code string `json:"code" binding:"required"`
}

func (h *BillingHandler) ValidateCoupon(c *gin.Context) {
	var req validateCouponRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	coupon, err := h.Store.GetCouponByCode(c, req.Code)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "COUPON_NOT_FOUND"}})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	now := time.Now()
	if !coupon.Active ||
		(coupon.ValidFrom.Valid && now.Before(coupon.ValidFrom.Time)) ||
		(coupon.ValidTo.Valid && now.After(coupon.ValidTo.Time)) ||
		(coupon.MaxRedemptions.Valid && coupon.RedeemedCount >= coupon.MaxRedemptions.Int32) {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "COUPON_INVALID"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"coupon": coupon})
}

func (h *BillingHandler) Distribution(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	attribution, err := h.Store.GetChannelAttributionByTenant(c, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusOK, gin.H{"attribution": nil})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"attribution": attribution})
}
