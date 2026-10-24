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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/billing"
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
	GetCouponRedemptionForOrder(ctx context.Context, arg store.GetCouponRedemptionForOrderParams) (store.CouponRedemptions, error)
	RedeemCoupon(ctx context.Context, arg store.RedeemCouponParams) (string, error)
	GetChannelAttributionByTenant(ctx context.Context, tenantID int64) (store.ChannelAttribution, error)
	CreateOrder(ctx context.Context, arg store.CreateOrderParams) (store.Orders, error)
	GetCheckoutOrderByIdempotencyKey(context.Context, store.GetCheckoutOrderByIdempotencyKeyParams) (store.Orders, error)
	ListOrdersByTenant(ctx context.Context, tenantID int64) ([]store.Orders, error)
	CancelPendingOrder(ctx context.Context, arg store.CancelPendingOrderParams) (store.Orders, error)
	ReleaseCouponRedemption(ctx context.Context, arg store.ReleaseCouponRedemptionParams) (int64, error)
	GetPaymentForTenant(ctx context.Context, arg store.GetPaymentForTenantParams) (store.Payments, error)
	CreateRefund(ctx context.Context, arg store.CreateRefundParams) (store.Refunds, error)
	ListRefundsByTenant(ctx context.Context, tenantID int64) ([]store.Refunds, error)
	GetInvoiceByID(ctx context.Context, arg store.GetInvoiceByIDParams) (store.Invoices, error)
	SetInvoicePDFPath(ctx context.Context, arg store.SetInvoicePDFPathParams) error
}

// BillingHandler serves checkout, invoice and subscription operations.
type BillingHandler struct {
	Store   BillingStore
	Pool    *db.Pool
	Stripe  *payment.Stripe
	BaseURL string
	// QuoteHMACKey signs billing quotes. Empty disables quote issuance;
	// no hardcoded default key exists.
	QuoteHMACKey []byte
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

// QuoteRequest specifies parameters for pre-checkout pricing calculation.
type QuoteRequest struct {
	PlanCode     string `json:"plan_code" binding:"required"`
	BillingCycle string `json:"billing_cycle" binding:"required"`
	CouponCode   string `json:"coupon_code"`
}

// quoteKey resolves only the explicitly injected quote signing key. Missing
// configuration is a server misconfiguration, never a downgrade to unsigned quotes.
func (h *BillingHandler) quoteKey() []byte {
	if len(h.QuoteHMACKey) > 0 {
		return h.QuoteHMACKey
	}
	return nil
}

// Quote returns an immutable tamper-evident quote before initiating checkout.
func (h *BillingHandler) Quote(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	var req QuoteRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.PlanCode) == "" || strings.TrimSpace(req.BillingCycle) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	quote, ok := h.buildQuote(c, q, tenantID, strings.TrimSpace(req.PlanCode), strings.TrimSpace(req.BillingCycle), strings.TrimSpace(req.CouponCode))
	if !ok {
		return
	}
	c.JSON(http.StatusOK, quote)
}

// buildQuote assembles a signed quote for a plan/cycle/coupon combination. It
// is shared by the quote endpoint and by checkout's legacy plan_code body so
// both paths price identically. On failure it has already written the HTTP
// error and returns false.
func (h *BillingHandler) buildQuote(c *gin.Context, q BillingStore, tenantID int64, planCode, cycle, couponCode string) (billing.Quote, bool) {
	plan, err := q.GetPlanByCode(c, planCode)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !plan.Active) {
		authError(c, http.StatusNotFound, "PLAN_NOT_FOUND")
		return billing.Quote{}, false
	}
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return billing.Quote{}, false
	}

	now := time.Now().UTC()
	var curEndTime time.Time
	sub, err := q.GetActiveSubscriptionByTenant(c, tenantID)
	if err == nil && sub.Status == billing.StatusActive && sub.PeriodEnd.Valid {
		// The default free subscription deliberately uses a 100-year sentinel;
		// it must not push paid checkout periods a century into the future.
		end := sub.PeriodEnd.Time.UTC()
		if end.After(now) && end.Before(now.AddDate(5, 0, 0)) {
			curEndTime = end
		}
	}
	periodStart, periodEnd, err := billing.CalculateRenewalPeriod(now, curEndTime, cycle)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_CYCLE", "detail": err.Error()}})
		return billing.Quote{}, false
	}

	couponKind, couponValue := "", int64(0)
	if code := strings.TrimSpace(couponCode); code != "" {
		coupon, couponErr := q.GetCouponByCode(c, code)
		if errors.Is(couponErr, pgx.ErrNoRows) {
			authError(c, http.StatusNotFound, "COUPON_NOT_FOUND")
			return billing.Quote{}, false
		}
		if couponErr != nil {
			authError(c, http.StatusInternalServerError, "DB_ERROR")
			return billing.Quote{}, false
		}
		info := billing.CouponInfo{
			Code:          coupon.Code,
			Kind:          coupon.Kind,
			Value:         coupon.Value,
			Active:        coupon.Active,
			RedeemedCount: coupon.RedeemedCount,
		}
		if coupon.MaxRedemptions.Valid && coupon.RedeemedCount >= coupon.MaxRedemptions.Int32 {
			authError(c, http.StatusConflict, "COUPON_EXHAUSTED")
			return billing.Quote{}, false
		}
		if coupon.MaxRedemptions.Valid {
			maxRedemptions := coupon.MaxRedemptions.Int32
			info.MaxRedemptions = &maxRedemptions
		}
		if coupon.ValidFrom.Valid {
			t := coupon.ValidFrom.Time
			info.ValidFrom = &t
		}
		if coupon.ValidTo.Valid {
			t := coupon.ValidTo.Time
			info.ValidTo = &t
		}
		if err := billing.ValidateCoupon(info, now); err != nil {
			authError(c, http.StatusConflict, "COUPON_INVALID")
			return billing.Quote{}, false
		}
		if coupon.Kind == billing.CouponKindPercent && (coupon.Value <= 0 || coupon.Value > 100) {
			authError(c, http.StatusConflict, "COUPON_INVALID")
			return billing.Quote{}, false
		}
		if coupon.Kind == billing.CouponKindFixed && coupon.Value <= 0 {
			authError(c, http.StatusConflict, "COUPON_INVALID")
			return billing.Quote{}, false
		}
		couponKind, couponValue = coupon.Kind, coupon.Value
	}

	key := h.quoteKey()
	if len(key) == 0 {
		authError(c, http.StatusInternalServerError, "QUOTE_KEY_UNCONFIGURED")
		return billing.Quote{}, false
	}

	quote, err := billing.BuildQuote(
		tenantID,
		strings.TrimSpace(couponCode),
		plan.Code,
		plan.PriceCents,
		cycle,
		plan.Currency,
		couponKind,
		couponValue,
		0,
		periodStart,
		periodEnd,
		30*time.Minute,
		key,
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "QUOTE_ERROR", "detail": err.Error()}})
		return billing.Quote{}, false
	}

	return quote, true
}

// CheckoutRequest carries either a signed quote (the documented flow) or the
// legacy plan_code body. The legacy shape is preserved for compatibility with
// api/v1-remediation.openapi.yaml: the server builds and signs the quote
// itself, so the tenant never gains a way to bypass pricing (M5 review 3.2).
type CheckoutRequest struct {
	Quote        *billing.Quote `json:"quote"`
	PlanCode     string         `json:"plan_code"`
	BillingCycle string         `json:"billing_cycle"`
	CouponCode   string         `json:"coupon_code"`
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
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	var quote billing.Quote
	if req.Quote != nil {
		quote = *req.Quote
		if quote.TenantID != tenantID || quote.PlanCode == "" {
			authError(c, http.StatusConflict, "QUOTE_INVALID")
			return
		}
		if err := quote.VerifyIntegrity(time.Now().UTC(), h.quoteKey()); err != nil {
			authError(c, http.StatusConflict, "QUOTE_INVALID")
			return
		}
	} else {
		// Legacy compatibility body: price it server-side so a tenant can never
		// bypass the signed-pricing path.
		if strings.TrimSpace(req.PlanCode) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
			return
		}
		cycle := strings.TrimSpace(req.BillingCycle)
		if cycle == "" {
			cycle = billing.CycleMonthly
		}
		built, ok := h.buildQuote(c, q, tenantID, strings.TrimSpace(req.PlanCode), cycle, strings.TrimSpace(req.CouponCode))
		if !ok {
			return
		}
		quote = built
	}
	plan, err := q.GetPlanByCode(c, quote.PlanCode)
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
	expectedBase, priceErr := billing.CalculateBasePrice(plan.PriceCents, quote.BillingCycle)
	discountsExceedBase := quote.DiscountCents > quote.BaseAmountCents || quote.ProratedCreditCents > quote.BaseAmountCents-quote.DiscountCents
	if quote.Currency != plan.Currency || priceErr != nil || quote.BaseAmountCents != expectedBase || quote.FinalAmountCents < 0 || quote.BaseAmountCents < 0 || quote.DiscountCents < 0 || quote.ProratedCreditCents < 0 || discountsExceedBase || quote.FinalAmountCents != quote.BaseAmountCents-quote.DiscountCents-quote.ProratedCreditCents || !quote.PeriodEnd.After(quote.PeriodStart) || quote.GeneratedAt.After(time.Now().UTC().Add(time.Minute)) || quote.PeriodStart.Before(quote.GeneratedAt.Add(-time.Minute)) || quote.PeriodStart.After(quote.GeneratedAt.Add(24*time.Hour)) || quote.PeriodEnd.After(quote.PeriodStart.AddDate(1, 0, 0)) {
		authError(c, http.StatusConflict, "QUOTE_INVALID")
		return
	}
	var order store.Orders
	existing := false
	if idempotency.Valid {
		order, err = q.GetCheckoutOrderByIdempotencyKey(c, store.GetCheckoutOrderByIdempotencyKeyParams{TenantID: tenantID, IdempotencyKey: idempotency})
		switch {
		case err == nil:
			if !order.PlanCode.Valid || order.PlanCode.String != plan.Code || order.AmountCents != quote.FinalAmountCents || order.Currency != quote.Currency || order.BillingCycle != quote.BillingCycle {
				authError(c, http.StatusConflict, "ORDER_EXISTS")
				return
			}
			redemption, redemptionErr := q.GetCouponRedemptionForOrder(c, store.GetCouponRedemptionForOrderParams{TenantID: tenantID, OrderID: pgtype.Int8{Int64: order.ID, Valid: true}})
			if redemptionErr != nil && !errors.Is(redemptionErr, pgx.ErrNoRows) {
				authError(c, http.StatusInternalServerError, "DB_ERROR")
				return
			}
			if quote.CouponCode == "" && redemptionErr == nil || quote.CouponCode != "" && (redemptionErr != nil || redemption.CouponCode != quote.CouponCode) {
				authError(c, http.StatusConflict, "ORDER_EXISTS")
				return
			}
			existing = true
		case !errors.Is(err, pgx.ErrNoRows):
			authError(c, http.StatusInternalServerError, "DB_ERROR")
			return
		}
	}
	// A fully discounted order needs no payment provider session. It is
	// settled synchronously through the manual provider after the checkout
	// transaction commits, so the tenant gets an active entitlement instead
	// of an unpayable pending order (M5 review 3.4 / M5-08 zero-amount rule).
	zeroAmount := quote.FinalAmountCents == 0
	if !existing {
		var channelID pgtype.Int8
		attribution, attributionErr := q.GetChannelAttributionByTenant(c, tenantID)
		if attributionErr == nil {
			channelID = pgtype.Int8{Int64: attribution.ChannelID, Valid: true}
		} else if !errors.Is(attributionErr, pgx.ErrNoRows) {
			authError(c, http.StatusInternalServerError, "DB_ERROR")
			return
		}
		order, err = q.CreateOrder(c, store.CreateOrderParams{TenantID: tenantID, OrderNo: fmt.Sprintf("ORD-%d-%d", tenantID, time.Now().UnixNano()), Kind: "new", Status: "pending", AmountCents: quote.FinalAmountCents, Currency: quote.Currency, PlanCode: pgtype.Text{String: plan.Code, Valid: true}, IdempotencyKey: idempotency, ChannelID: channelID, BillingCycle: quote.BillingCycle})
		if err != nil {
			authError(c, http.StatusConflict, "ORDER_EXISTS")
			return
		}
	}
	if !existing && quote.CouponCode != "" {
		result, redeemErr := q.RedeemCoupon(c, store.RedeemCouponParams{Code: quote.CouponCode, OrderID: pgtype.Int8{Int64: order.ID, Valid: true}})
		if redeemErr != nil {
			authError(c, http.StatusInternalServerError, "DB_ERROR")
			return
		}
		switch result {
		case "redeemed":
		case "exhausted":
			authError(c, http.StatusConflict, "COUPON_EXHAUSTED")
			return
		case "already_redeemed":
			authError(c, http.StatusConflict, "COUPON_ALREADY_REDEEMED")
			return
		default:
			authError(c, http.StatusConflict, "COUPON_INVALID")
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
		if zeroAmount && order.Status == "pending" {
			if settleErr := SettleOrder(c.Request.Context(), h.Pool, order.ID); settleErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "ORDER_SETTLE_FAILED"}, "order_id": order.ID})
				return
			}
			order.Status = "paid"
			c.JSON(http.StatusCreated, gin.H{"order": order, "payment_method": "manual"})
			return
		}
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

// CancelOrder withdraws a pending order and releases its coupon reservation,
// so an abandoned checkout cannot burn a limited coupon forever (M5 review 3.4).
// Settlement orders are immutable: only a pending order can be canceled.
func (h *BillingHandler) CancelOrder(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	orderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || orderID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	order, err := q.CancelPendingOrder(c, store.CancelPendingOrderParams{TenantID: tenantID, ID: orderID})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusNotFound, "ORDER_NOT_FOUND")
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	redemption, redemptionErr := q.GetCouponRedemptionForOrder(c, store.GetCouponRedemptionForOrderParams{TenantID: tenantID, OrderID: pgtype.Int8{Int64: orderID, Valid: true}})
	if redemptionErr == nil {
		if _, releaseErr := q.ReleaseCouponRedemption(c, store.ReleaseCouponRedemptionParams{
			CouponCode: redemption.CouponCode, TenantID: tenantID, OrderID: pgtype.Int8{Int64: orderID, Valid: true},
		}); releaseErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
			return
		}
	} else if !errors.Is(redemptionErr, pgx.ErrNoRows) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"order": order, "coupon_released": redemptionErr == nil})
}

// cancelAbandonedOrder cancels a pending order from a provider terminal event
// (async payment failed / checkout session expired) and hands the coupon
// quota back. It runs on the platform pool because the webhook has no tenant
// session; the reservation guard trigger still refuses to release a settled
// order's coupon.
func (h *BillingHandler) cancelAbandonedOrder(ctx context.Context, orderID int64) {
	tx, err := h.Pool.PlatformTx(ctx)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := store.New(tx)
	order, err := queries.PlatformCancelPendingOrder(ctx, orderID)
	if err != nil {
		return
	}
	couponCode := ""
	redemption, redemptionErr := queries.GetCouponRedemptionForOrder(ctx, store.GetCouponRedemptionForOrderParams{
		TenantID: order.TenantID, OrderID: pgtype.Int8{Int64: order.ID, Valid: true},
	})
	if redemptionErr == nil {
		couponCode = redemption.CouponCode
	}
	if couponCode != "" {
		_, _ = queries.PlatformReleaseCouponRedemption(ctx, store.PlatformReleaseCouponRedemptionParams{
			CouponCode: couponCode,
			OrderID:    pgtype.Int8{Int64: orderID, Valid: true},
		})
	}
	_ = tx.Commit(ctx)
}

// RefundRequest captures a tenant refund request for a successful payment.
type RefundRequest struct {
	PaymentID   int64  `json:"payment_id" binding:"required"`
	RefundNo    string `json:"refund_no" binding:"required"`
	AmountCents int64  `json:"amount_cents" binding:"required"`
	Reason      string `json:"reason"`
}

// CreateRefundRequest records a refund request against a successful payment.
// The reservation routine enforces the outstanding-refund ceiling and refuses
// a tenant id that differs from the session (M5 review P0-2).
func (h *BillingHandler) CreateRefundRequest(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	var req RefundRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.RefundNo) == "" || req.AmountCents <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	// The order is resolved from the payment so the caller cannot address
	// another tenant's payment id even by mistake.
	payment, err := q.GetPaymentForTenant(c, store.GetPaymentForTenantParams{TenantID: tenantID, ID: req.PaymentID})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusNotFound, "REFUND_PAYMENT_NOT_FOUND")
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if !payment.OrderID.Valid {
		authError(c, http.StatusConflict, "REFUND_PAYMENT_NOT_SUCCESSFUL")
		return
	}
	created, err := q.CreateRefund(c, store.CreateRefundParams{
		TenantID:    tenantID,
		OrderID:     payment.OrderID.Int64,
		PaymentID:   req.PaymentID,
		RefundNo:    strings.TrimSpace(req.RefundNo),
		AmountCents: req.AmountCents,
		Currency:    payment.Currency,
		Reason:      req.Reason,
	})
	if err != nil {
		mapRefundError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"refund": created})
}

// ListRefundRequests returns the tenant's refund requests, newest first.
func (h *BillingHandler) ListRefundRequests(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	refunds, err := q.ListRefundsByTenant(c, tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"refunds": refunds})
}

// mapRefundError translates the reservation routine's distinct rejection
// conditions into API error codes instead of one opaque empty result
// (M5 review 3.32).
func mapRefundError(c *gin.Context, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Message {
		case "REFUND_EXCEEDS_PAYMENT":
			authError(c, http.StatusConflict, "REFUND_EXCEEDS_PAYMENT")
			return
		case "REFUND_DUPLICATE":
			authError(c, http.StatusConflict, "REFUND_DUPLICATE")
			return
		case "REFUND_CURRENCY_MISMATCH":
			authError(c, http.StatusConflict, "REFUND_CURRENCY_MISMATCH")
			return
		case "REFUND_PAYMENT_NOT_SUCCESSFUL":
			authError(c, http.StatusConflict, "REFUND_PAYMENT_NOT_SUCCESSFUL")
			return
		case "REFUND_PAYMENT_NOT_FOUND", "REFUND_TENANT_MISMATCH", "REFUND_TENANT_REQUIRED":
			authError(c, http.StatusForbidden, "REFUND_TENANT_MISMATCH")
			return
		case "REFUND_INVALID_PARAMS":
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
			return
		}
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
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
	event, err := h.Stripe.ParseAndVerifyWebhook(c.Request.Context(), c.Request.Header, raw)
	if err != nil {
		// Permanent failures are answered 4xx so Stripe stops retrying the
		// same delivery; classification uses sentinel errors, never string
		// matching on error text.
		switch {
		case errors.Is(err, payment.ErrPaymentNotPaid):
			c.JSON(http.StatusOK, gin.H{"status": "pending_payment"})
		case errors.Is(err, payment.ErrInvalidSignature), errors.Is(err, payment.ErrSignatureExpired):
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_SIGNATURE"}})
		case errors.Is(err, payment.ErrWebhookNotConfigured):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "PAYMENT_PROVIDER_DISABLED"}})
		default: // payment.ErrInvalidPayload and unclassified parse failures
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PAYLOAD"}})
		}
		return
	}
	switch event.EventType {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded", "payment_intent.succeeded":
		// Settlement-bearing events continue to the idempotent settle path.
	case "checkout.session.async_payment_failed", "checkout.session.expired":
		// Terminal non-payment events: the customer abandoned the checkout or
		// the delayed payment failed. Cancel the pending order and hand the
		// reserved coupon quota back, otherwise the order stays pending forever
		// (M5 review 3.3). Always 200 so Stripe stops redelivering.
		if event.OrderID > 0 {
			h.cancelAbandonedOrder(c.Request.Context(), event.OrderID)
		}
		c.JSON(http.StatusOK, gin.H{"status": "canceled"})
		return
	default:
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}
	if event.OrderID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PAYLOAD"}})
		return
	}
	err = SettleOrderWithDetails(c.Request.Context(), h.Pool, event.OrderID, SettleOptions{
		Provider:      payment.ProviderStripe,
		ProviderTxnID: event.ProviderTxnID,
		EventID:       event.EventID,
		AmountCents:   event.AmountCents,
		Currency:      event.Currency,
		RawPayload:    raw,
	})
	if err != nil {
		// Amount/currency verification failures and unknown orders are
		// permanent: answer 4xx so the provider does not redeliver forever.
		switch {
		case errors.Is(err, ErrPaymentVerificationFailed):
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "PAYMENT_VERIFICATION_FAILED"}})
		case errors.Is(err, ErrOrderNotFound):
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "ORDER_NOT_FOUND"}})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		}
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
