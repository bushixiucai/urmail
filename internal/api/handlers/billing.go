package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/pdf"
	"github.com/bushixiucai/urmail/internal/store"
)

type BillingStore interface {
	ListActivePlans(ctx context.Context) ([]store.Plans, error)
	GetPlanByCode(ctx context.Context, code string) (store.Plans, error)
	GetActiveSubscriptionByTenant(ctx context.Context, tenantID int64) (store.Subscriptions, error)
	ListInvoicesByTenant(ctx context.Context, tenantID int64) ([]store.Invoices, error)
	GetCouponByCode(ctx context.Context, code string) (store.Coupons, error)
	GetChannelAttributionByTenant(ctx context.Context, tenantID int64) (store.ChannelAttribution, error)
	CreateOrder(ctx context.Context, arg store.CreateOrderParams) (store.Orders, error)
	ListOrdersByTenant(ctx context.Context, tenantID int64) ([]store.Orders, error)
	GetInvoiceByID(ctx context.Context, arg store.GetInvoiceByIDParams) (store.Invoices, error)
	SetInvoicePDFPath(ctx context.Context, arg store.SetInvoicePDFPathParams) error
}

type BillingHandler struct {
	Store   BillingStore
	Pool    *db.Pool
	Stripe  *payment.Stripe
	BaseURL string
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
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	sub, err := q.GetActiveSubscriptionByTenant(c, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusOK, gin.H{"subscription": nil, "plan": nil})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	plan, err := q.GetPlanByCode(c, sub.PlanID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"subscription": sub, "plan": plan})
}

func (h *BillingHandler) Invoices(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	invoices, err := q.ListInvoicesByTenant(c, tenantID)
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
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	attribution, err := q.GetChannelAttributionByTenant(c, tenantID)
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

type CheckoutRequest struct {
	PlanCode string `json:"plan_code" binding:"required"`
}

func (h *BillingHandler) Checkout(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	var req CheckoutRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.PlanCode) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	plan, err := q.GetPlanByCode(c, req.PlanCode)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "PLAN_NOT_FOUND"}})
		return
	}
	var idempotency pgtype.Text
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		idempotency = pgtype.Text{String: key, Valid: true}
	}
	order, err := q.CreateOrder(c, store.CreateOrderParams{
		TenantID:       tenantID,
		OrderNo:        fmt.Sprintf("ORD-%d-%d", tenantID, time.Now().UnixNano()),
		Kind:           "new",
		Status:         "pending",
		AmountCents:    plan.PriceCents,
		Currency:       plan.Currency,
		PlanCode:       pgtype.Text{String: plan.Code, Valid: true},
		IdempotencyKey: idempotency,
	})
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "ORDER_EXISTS"}})
		return
	}
	if h.Stripe != nil {
		base := strings.TrimRight(h.BaseURL, "/")
		session, err := h.Stripe.CreateCheckoutSession(c, order.ID, order.OrderNo,
			order.AmountCents, order.Currency, base+"/billing?paid=1", base+"/billing?canceled=1")
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "PAYMENT_PROVIDER_ERROR"}})
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"order":          order,
			"payment_method": "stripe",
			"checkout_url":   session.URL,
		})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"order":          order,
		"payment_method": "manual",
		"message":        "order created; settle it from the platform console or a payment provider webhook",
	})
}

func (h *BillingHandler) Orders(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	orders, err := q.ListOrdersByTenant(c, tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"orders": orders})
}

func (h *BillingHandler) StripeWebhook(c *gin.Context) {
	if h.Stripe == nil || h.Pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "PAYMENT_PROVIDER_DISABLED"}})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PAYLOAD"}})
		return
	}
	if err := h.Stripe.VerifyWebhook(raw, c.GetHeader("Stripe-Signature"), 5*time.Minute); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_SIGNATURE"}})
		return
	}
	var event payment.Event
	if err := json.Unmarshal(raw, &event); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PAYLOAD"}})
		return
	}
	if event.Type != "checkout.session.completed" {
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}
	id, err := strconv.ParseInt(event.Data.Object.Metadata.OrderID, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PAYLOAD"}})
		return
	}
	if err := SettleOrder(c, h.Pool, id); err != nil && !errors.Is(err, ErrOrderNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"received": true})
}

func (h *BillingHandler) InvoicePDF(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	q := requestStore(c, h.Store)
	invoice, err := q.GetInvoiceByID(c, store.GetInvoiceByIDParams{TenantID: tenantID, ID: id})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "INVOICE_NOT_FOUND"}})
		return
	}
	path := invoice.PdfPath.String
	if !invoice.PdfPath.Valid || path == "" {
		raw, err := pdf.Invoice(pdf.InvoiceData{
			Number: invoice.Number, Tenant: fmt.Sprintf("tenant-%d", tenantID),
			Amount:      fmt.Sprintf("%.2f", float64(invoice.AmountCents)/100),
			Currency:    invoice.Currency,
			PeriodStart: invoice.PeriodStart.Time.Format("2006-01-02"),
			PeriodEnd:   invoice.PeriodEnd.Time.Format("2006-01-02"),
			Status:      invoice.Status, IssuedAt: invoice.IssuedAt.Time.Format("2006-01-02"),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "PDF_FAILED"}})
			return
		}
		dir := "/var/lib/urmail/invoices"
		if err := os.MkdirAll(dir, 0o700); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "PDF_FAILED"}})
			return
		}
		path = filepath.Join(dir, invoice.Number+".pdf")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "PDF_FAILED"}})
			return
		}
		if err := q.SetInvoicePDFPath(c, store.SetInvoicePDFPathParams{
			TenantID: tenantID, ID: id, PdfPath: pgtype.Text{String: path, Valid: true},
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
			return
		}
	}
	c.File(path)
}
