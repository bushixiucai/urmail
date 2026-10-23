package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
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

// BillingStore supplies tenant billing reads and order persistence.
type BillingStore interface {
	ListActivePlans(ctx context.Context) ([]store.Plans, error)
	GetPlanByCode(ctx context.Context, code string) (store.Plans, error)
	GetActiveSubscriptionByTenant(ctx context.Context, tenantID int64) (store.Subscriptions, error)
	ListInvoicesByTenant(ctx context.Context, tenantID int64) ([]store.Invoices, error)
	GetCouponByCode(ctx context.Context, code string) (store.Coupons, error)
	GetChannelAttributionByTenant(ctx context.Context, tenantID int64) (store.ChannelAttribution, error)
	CreateOrder(ctx context.Context, arg store.CreateOrderParams) (store.Orders, error)
	GetCheckoutOrderByIdempotencyKey(context.Context, store.GetCheckoutOrderByIdempotencyKeyParams) (store.Orders, error)
	ListOrdersByTenant(ctx context.Context, tenantID int64) ([]store.Orders, error)
	GetInvoiceByID(ctx context.Context, arg store.GetInvoiceByIDParams) (store.Invoices, error)
	SetInvoicePDFPath(ctx context.Context, arg store.SetInvoicePDFPathParams) error
}

// BillingHandler serves checkout, invoice and subscription operations.
type BillingHandler struct {
	Store   BillingStore
	Pool    *db.Pool
	Stripe  *payment.Stripe
	BaseURL string
}

// NewBillingHandler constructs a billing handler with explicit dependencies.
func NewBillingHandler(s BillingStore) *BillingHandler {
	return &BillingHandler{Store: s}
}

// PlanResponse keeps JSONB fields as JSON objects instead of base64 strings.
type PlanResponse struct {
	Code           string          `json:"code"`
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
	Active         bool            `json:"active"`
}

func newPlanResponse(plan store.Plans) PlanResponse {
	return PlanResponse{Code: plan.Code, Name: json.RawMessage(plan.Name), Description: json.RawMessage(plan.Description), PriceCents: plan.PriceCents, Currency: plan.Currency, QuotaBytes: plan.QuotaBytes, MailboxLimit: plan.MailboxLimit, DomainLimit: plan.DomainLimit, DailySendLimit: plan.DailySendLimit, Features: json.RawMessage(plan.Features), SortOrder: plan.SortOrder, Active: plan.Active}
}
func planResponses(plans []store.Plans) []PlanResponse {
	out := make([]PlanResponse, 0, len(plans))
	for _, plan := range plans {
		out = append(out, newPlanResponse(plan))
	}
	return out
}

// Plans lists active plans for tenant checkout.
func (h *BillingHandler) Plans(c *gin.Context) {
	plans, err := h.Store.ListActivePlans(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plans": planResponses(plans)})
}

// Subscription returns the tenant's current subscription and plan.
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
	c.JSON(http.StatusOK, gin.H{"subscription": sub, "plan": newPlanResponse(plan)})
}

// Invoices lists invoices belonging to the current tenant.
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

// ValidateCoupon checks the current validity of a coupon.
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

// Distribution returns the current tenant's channel attribution.
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

// CheckoutRequest identifies the selected plan.
type CheckoutRequest struct {
	PlanCode string `json:"plan_code" binding:"required"`
}

// Checkout commits a resumable order before contacting a payment provider.
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
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !plan.Active) {
		authError(c, http.StatusNotFound, "PLAN_NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	var idempotency pgtype.Text
	if key := strings.TrimSpace(c.GetHeader("Idempotency-Key")); key != "" {
		if len(key) > 200 {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
			return
		}
		idempotency = pgtype.Text{String: key, Valid: true}
	}
	var order store.Orders
	existing := false
	if idempotency.Valid {
		order, err = q.GetCheckoutOrderByIdempotencyKey(c, store.GetCheckoutOrderByIdempotencyKeyParams{TenantID: tenantID, IdempotencyKey: idempotency})
		switch {
		case err == nil:
			if !order.PlanCode.Valid || order.PlanCode.String != plan.Code {
				authError(c, http.StatusConflict, "ORDER_EXISTS")
				return
			}
			existing = true
		case !errors.Is(err, pgx.ErrNoRows):
			authError(c, http.StatusInternalServerError, "DB_ERROR")
			return
		}
	}
	if !existing {
		var channelID pgtype.Int8
		attribution, attributionErr := q.GetChannelAttributionByTenant(c, tenantID)
		if attributionErr == nil {
			channelID = pgtype.Int8{Int64: attribution.ChannelID, Valid: true}
		} else if !errors.Is(attributionErr, pgx.ErrNoRows) {
			authError(c, http.StatusInternalServerError, "DB_ERROR")
			return
		}
		order, err = q.CreateOrder(c, store.CreateOrderParams{TenantID: tenantID, OrderNo: fmt.Sprintf("ORD-%d-%d", tenantID, time.Now().UnixNano()), Kind: "new", Status: "pending", AmountCents: plan.PriceCents, Currency: plan.Currency, PlanCode: pgtype.Text{String: plan.Code, Valid: true}, IdempotencyKey: idempotency, ChannelID: channelID})
		if err != nil {
			authError(c, http.StatusConflict, "ORDER_EXISTS")
			return
		}
	}
	if order.Status != "pending" && order.Status != "paid" {
		authError(c, http.StatusConflict, "ORDER_EXISTS")
		return
	}
	// The order must exist durably before any provider request. A failed provider
	// response leaves a discoverable pending order; the same client key resumes it.
	// The provider also receives a stable key, so a lost response does not create
	// another hosted session while the provider retains its idempotency record.
	middleware.AfterCommit(c, func() {
		if h.Stripe != nil && order.Status == "pending" {
			base := strings.TrimRight(h.BaseURL, "/")
			session, providerErr := h.Stripe.CreateCheckoutSession(c.Request.Context(), order.ID, order.OrderNo, order.AmountCents, order.Currency, base+"/billing?paid=1", base+"/billing?canceled=1")
			if providerErr != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "PAYMENT_PROVIDER_ERROR"}, "order_id": order.ID})
				return
			}
			c.JSON(http.StatusCreated, gin.H{"order": order, "payment_method": "stripe", "checkout_url": session.URL})
			return
		}
		method := "manual"
		if h.Stripe != nil {
			method = "stripe"
		}
		c.JSON(http.StatusCreated, gin.H{"order": order, "payment_method": method})
	})
}

// Orders lists recent orders belonging to the current tenant.
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

// StripeWebhook validates the signed event before idempotent settlement.
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
	if err := SettleOrder(c.Request.Context(), h.Pool, id); err != nil && !errors.Is(err, ErrOrderNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"received": true})
}

// InvoicePDF renders an authorized invoice directly as a PDF attachment.
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
	// An invoice is derived from the persisted ledger. Render it directly;
	// a writable global cache and a database mutation on GET are unnecessary.
	raw, err := pdf.Invoice(pdf.InvoiceData{Number: invoice.Number, Tenant: fmt.Sprintf("tenant-%d", tenantID), Amount: fmt.Sprintf("%.2f", float64(invoice.AmountCents)/100), Currency: invoice.Currency, PeriodStart: invoice.PeriodStart.Time.Format("2006-01-02"), PeriodEnd: invoice.PeriodEnd.Time.Format("2006-01-02"), Status: invoice.Status, IssuedAt: invoice.IssuedAt.Time.Format("2006-01-02")})
	if err != nil {
		authError(c, http.StatusInternalServerError, "PDF_FAILED")
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": invoice.Number + ".pdf"}))
	c.Header("Cache-Control", "private, no-store")
	c.Data(http.StatusOK, "application/pdf", raw)
}
