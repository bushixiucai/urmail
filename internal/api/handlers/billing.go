package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/billingrenew"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/payment"
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
	ListRefundsByTenant(ctx context.Context, tenantID int64) ([]store.ListRefundsByTenantRow, error)
	GetInvoiceByID(ctx context.Context, arg store.GetInvoiceByIDParams) (store.Invoices, error)
	SetInvoicePDFPath(ctx context.Context, arg store.SetInvoicePDFPathParams) error
}

// BillingHandler serves checkout, invoice and subscription operations.
type BillingHandler struct {
	Store   BillingStore
	Pool    *db.Pool
	Stripe  *payment.Stripe
	Gateway *PaymentGatewayHandler
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
	Code           string                    `json:"code"`
	Name           json.RawMessage           `json:"name"`
	Description    json.RawMessage           `json:"description"`
	PriceCents     int64                     `json:"price_cents"`
	Currency       string                    `json:"currency"`
	QuotaBytes     int64                     `json:"quota_bytes"`
	MailboxLimit   int32                     `json:"mailbox_limit"`
	DomainLimit    int32                     `json:"domain_limit"`
	DailySendLimit int32                     `json:"daily_send_limit"`
	Features       json.RawMessage           `json:"features"`
	SortOrder      int32                     `json:"sort_order"`
	Active         bool                      `json:"active"`
	Prices         []store.ListPlanPricesRow `json:"prices,omitempty"`
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
	offers := make([]store.Plans, 0, len(plans))
	for _, plan := range plans {
		if purchasablePlan(plan) {
			offers = append(offers, plan)
		}
	}
	response := planResponses(offers)
	if queries, ok := h.Store.(*store.Queries); ok {
		for i := range response {
			prices, priceErr := queries.ListPlanPrices(c, response[i].Code)
			if priceErr != nil {
				authError(c, 500, "DB_ERROR")
				return
			}
			response[i].Prices = prices
		}
	}
	c.JSON(http.StatusOK, gin.H{"plans": response})
}

// Subscription returns the tenant's current subscription and plan.
func (h *BillingHandler) Subscription(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	role := middleware.GetRole(c)
	canManageBilling := role == "owner" || role == "admin" || role == "billing"
	selfService := false
	if queries, ok := q.(*store.Queries); ok {
		var lookupErr error
		selfService, lookupErr = queries.IsSelfServiceTenant(c, tenantID)
		if lookupErr != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
	}
	sub, err := q.GetActiveSubscriptionByTenant(c, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusOK, gin.H{"tenant_id": tenantID, "self_service": selfService, "can_manage_billing": canManageBilling, "subscription": nil, "plan": nil})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	plan, err := subscriptionPlan(c, q, sub)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	sub.PlanID = plan.Code
	if queries, ok := q.(*store.Queries); ok {
		if term, termErr := queries.GetCurrentSubscriptionTerm(c, tenantID); termErr == nil {
			sub.BillingCycle = term.BillingCycle
		}
	}

	var change any
	if queries, ok := q.(*store.Queries); ok && canManageBilling {
		row, lookupErr := queries.GetSubscriptionChangeStatus(c, tenantID)
		if lookupErr == nil {
			change = row
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			authError(c, 500, "DB_ERROR")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"tenant_id": tenantID, "self_service": selfService, "can_manage_billing": canManageBilling, "subscription": sub, "plan": newPlanResponse(plan), "scheduled_change": change})
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
	coupon, err := requestStore(c, h.Store).GetCouponByCode(c, req.Code)
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
	c.JSON(http.StatusOK, gin.H{"coupon": gin.H{"code": coupon.Code, "kind": coupon.Kind, "value": coupon.Value, "max_redemptions": coupon.MaxRedemptions, "redeemed_count": coupon.RedeemedCount, "valid_from": coupon.ValidFrom, "valid_to": coupon.ValidTo, "active": coupon.Active, "currency": coupon.Currency}})
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
	DomainID     int64  `json:"domain_id"`
	PlanCode     string `json:"plan_code" binding:"required"`
	BillingCycle string `json:"billing_cycle" binding:"required"`
	Currency     string `json:"currency"`
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
	quote, ok := h.buildQuoteCurrency(c, q, tenantID, strings.TrimSpace(req.PlanCode), strings.TrimSpace(req.BillingCycle), strings.TrimSpace(req.CouponCode), strings.ToUpper(strings.TrimSpace(req.Currency)), req.DomainID)
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
	return h.buildQuoteCurrency(c, q, tenantID, planCode, cycle, couponCode, "")
}

func (h *BillingHandler) buildQuoteCurrency(c *gin.Context, q BillingStore, tenantID int64, planCode, cycle, couponCode, currency string, domainIDs ...int64) (billing.Quote, bool) {
	domainID := int64(0)
	if len(domainIDs) > 0 {
		domainID = domainIDs[0]
	}
	if queries, ok := q.(*store.Queries); ok {
		resolved, resolveErr := queries.ResolvePurchaseDomain(c, store.ResolvePurchaseDomainParams{TenantID: tenantID, DomainID: domainID})
		if errors.Is(resolveErr, pgx.ErrNoRows) {
			authError(c, 409, "DOMAIN_VERIFICATION_REQUIRED")
			return billing.Quote{}, false
		}
		if resolveErr != nil {
			authError(c, 500, "DB_ERROR")
			return billing.Quote{}, false
		}
		domainID = resolved
	}
	if queries, ok := q.(*store.Queries); ok {
		allowed, err := queries.SelfServicePurchaseAllowed(c, store.SelfServicePurchaseAllowedParams{TenantID: tenantID, ID: domainID})
		if err != nil {
			authError(c, 500, "DB_ERROR")
			return billing.Quote{}, false
		}
		if !allowed.Valid || !allowed.Bool {
			authError(c, 409, "DOMAIN_VERIFICATION_REQUIRED")
			return billing.Quote{}, false
		}
	}
	plan, err := q.GetPlanByCode(c, planCode)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !plan.Active) {
		authError(c, http.StatusNotFound, "PLAN_NOT_FOUND")
		return billing.Quote{}, false
	}
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return billing.Quote{}, false
	}

	if !purchasablePlan(plan) {
		authError(c, 403, "FREE_PLAN_PLATFORM_ONLY")
		return billing.Quote{}, false
	}
	catalogCurrency := plan.Currency
	if currency == "" {
		currency = plan.Currency
	}
	if currency != "CNY" && currency != "USD" {
		authError(c, 400, "INVALID_CURRENCY")
		return billing.Quote{}, false
	}
	plan.Currency = currency
	if queries, ok := q.(*store.Queries); ok {
		snapshot, _ := json.Marshal(newPlanResponse(plan))
		fits, fitErr := queries.PlanSnapshotFitsResources(c, store.PlanSnapshotFitsResourcesParams{TenantID: tenantID, PlanSnapshot: snapshot})
		if fitErr != nil {
			authError(c, 500, "DB_ERROR")
			return billing.Quote{}, false
		}
		if !fits {
			authError(c, 409, "PLAN_CAPACITY_EXCEEDED")
			return billing.Quote{}, false
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	var curEndTime time.Time
	sub, subErr := q.GetActiveSubscriptionByTenant(c, tenantID)
	if subErr != nil && !errors.Is(subErr, pgx.ErrNoRows) {
		authError(c, 500, "DB_ERROR")
		return billing.Quote{}, false
	}
	effectiveCode := sub.PlanID
	scheduledChange := false
	futureContinuation := false
	paidCurrent := subErr == nil && sub.Status != billing.StatusTrialing && sub.PeriodEnd.Valid && sub.PeriodEnd.Time.After(now)
	if queries, ok := q.(*store.Queries); ok && subErr == nil {
		effective, effectiveErr := queries.GetEffectivePlan(c, tenantID)
		if effectiveErr != nil {
			authError(c, 500, "DB_ERROR")
			return billing.Quote{}, false
		}
		effectiveCode = effective.Code
		if paidCurrent && effectiveCode != "free" {
			future, futureErr := queries.LastFuturePaidPlan(c, tenantID)
			if futureErr != nil && !errors.Is(futureErr, pgx.ErrNoRows) {
				authError(c, 500, "DB_ERROR")
				return billing.Quote{}, false
			}
			futureContinuation = futureErr == nil && future.PlanCode == plan.Code

			change, changeErr := queries.GetSubscriptionChange(c, tenantID)
			if changeErr != nil && !errors.Is(changeErr, pgx.ErrNoRows) {
				authError(c, 500, "DB_ERROR")
				return billing.Quote{}, false
			}
			scheduledChange = changeErr == nil && change.TargetPlanCode == plan.Code && change.EffectiveAt.Valid && change.EffectiveAt.Time.Equal(sub.PeriodEnd.Time)
			if futureErr == nil && !futureContinuation && !scheduledChange {
				authError(c, 409, "PLAN_CHANGE_REVIEW_REQUIRED")
				return billing.Quote{}, false
			}

			if scheduledChange {
				snapshot, _ := json.Marshal(newPlanResponse(plan))
				fits, fitErr := queries.PlanSnapshotFitsResources(c, store.PlanSnapshotFitsResourcesParams{TenantID: tenantID, PlanSnapshot: snapshot})
				if fitErr != nil {
					authError(c, 500, "DB_ERROR")
					return billing.Quote{}, false
				}
				if !fits {
					authError(c, 409, "PLAN_CAPACITY_EXCEEDED")
					return billing.Quote{}, false
				}
			}
		}
	}
	paidCurrent = paidCurrent && effectiveCode != "free"
	if paidCurrent && (effectiveCode == plan.Code || scheduledChange || futureContinuation) {
		curEndTime = sub.PeriodEnd.Time.UTC()
	}
	var saleCalendar *billing.CalendarSpec
	if queries, ok := q.(*store.Queries); ok {
		saleCalendar, err = quoteBillingCalendar(c.Request.Context(), queries, tenantID, now, paidCurrent)
		if err != nil {
			if errors.Is(err, errBillingCalendarReview) {
				authError(c, 409, "BILLING_CALENDAR_REVIEW_REQUIRED")
			} else {
				authError(c, 500, "BILLING_CALENDAR_INVALID")
			}
			return billing.Quote{}, false
		}
	}
	periodStart, periodEnd, err := billing.CalculateRenewalPeriod(now, curEndTime, cycle)
	if saleCalendar != nil {
		anchor, anchorErr := saleCalendar.Anchor()
		if anchorErr != nil {
			err = anchorErr
		} else {
			periodStart, periodEnd, err = billing.CalculateAnchoredRenewalPeriod(now, curEndTime, cycle, anchor)
		}
	}
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
		if coupon.Kind == "trial_days" {
			authError(c, http.StatusConflict, "COUPON_KIND_UNSUPPORTED")
			return billing.Quote{}, false
		}
		if coupon.Kind == "fixed" && (!coupon.Currency.Valid || coupon.Currency.String != plan.Currency) {
			authError(c, http.StatusConflict, "COUPON_CURRENCY_MISMATCH")
			return billing.Quote{}, false
		}
		couponKind, couponValue = coupon.Kind, coupon.Value
	}

	key := h.quoteKey()
	if len(key) == 0 {
		authError(c, http.StatusInternalServerError, "QUOTE_KEY_UNCONFIGURED")
		return billing.Quote{}, false
	}

	price, version := plan.PriceCents, int64(0)
	if queries, ok := q.(*store.Queries); ok {
		row, priceErr := queries.GetConfiguredPlanPrice(c, store.GetConfiguredPlanPriceParams{PlanCode: plan.Code, Currency: plan.Currency, BillingCycle: cycle})
		switch {
		case priceErr == nil:
			price, version = row.AmountCents, row.Version
		case !errors.Is(priceErr, pgx.ErrNoRows):
			authError(c, 500, "DB_ERROR")
			return billing.Quote{}, false
		case cycle != billing.CycleMonthly || currency != catalogCurrency:
			authError(c, 409, "PRICE_UNCONFIGURED")
			return billing.Quote{}, false
		}
	} else if cycle != billing.CycleMonthly {
		authError(c, 409, "PRICE_UNCONFIGURED")
		return billing.Quote{}, false
	}
	kind := "new"
	if !curEndTime.IsZero() {
		kind = "renew"
	}
	if scheduledChange && effectiveCode != plan.Code {
		kind = "downgrade"
	}
	configuredTotal := price
	if paidCurrent && effectiveCode != plan.Code && !scheduledChange && !futureContinuation {
		if couponCode != "" {
			authError(c, 409, "COUPON_INVALID")
			return billing.Quote{}, false
		}
		queries, ok := q.(*store.Queries)
		if !ok {
			authError(c, 409, "PLAN_CHANGE_REVIEW_REQUIRED")
			return billing.Quote{}, false
		}
		term, termErr := queries.GetCurrentSubscriptionTerm(c, tenantID)
		if termErr != nil || term.Currency != currency || term.BillingCycle != cycle || sub.PeriodEnd.Time.After(term.PeriodEnd.Time.Add(time.Second)) {
			authError(c, 409, "PLAN_CHANGE_REVIEW_REQUIRED")
			return billing.Quote{}, false
		}
		basis := term.OrderID
		if term.BasisOrderID.Valid {
			basis = term.BasisOrderID.Int64
		}
		pending, pendingErr := queries.PendingRefundForTerm(c, store.PendingRefundForTermParams{TenantID: tenantID, TargetTermOrderID: pgtype.Int8{Int64: basis, Valid: true}})
		if pendingErr != nil {
			authError(c, 500, "DB_ERROR")
			return billing.Quote{}, false
		}
		if pending {
			authError(c, 409, "PLAN_CHANGE_REVIEW_REQUIRED")
			return billing.Quote{}, false
		}
		currentPlan, planErr := subscriptionPlan(c, q, sub)
		limitAtLeast := func(next, old int64) bool { return next == -1 || (old != -1 && next >= old) }
		if planErr != nil || !billing.UpgradeCompatible(currentPlan.Code, currentPlan.Features, plan.Features) || !limitAtLeast(plan.QuotaBytes, currentPlan.QuotaBytes) || !limitAtLeast(int64(plan.MailboxLimit), int64(currentPlan.MailboxLimit)) || !limitAtLeast(int64(plan.DomainLimit), int64(currentPlan.DomainLimit)) || !limitAtLeast(int64(plan.DailySendLimit), int64(currentPlan.DailySendLimit)) {
			authError(c, 409, "PLAN_CHANGE_REVIEW_REQUIRED")
			return billing.Quote{}, false
		}
		difference, diffErr := billing.UpgradeDifference(term.BaseAmountCents, configuredTotal, term.BillingPeriodStart.Time, term.PeriodEnd.Time, now)
		if diffErr != nil {
			authError(c, 409, "PLAN_CHANGE_SCHEDULE_REQUIRED")
			return billing.Quote{}, false
		}
		kind = "upgrade"
		price = difference
		periodStart = now
		periodEnd = term.PeriodEnd.Time
	}
	quote, err := billing.BuildPricedQuote(
		tenantID,
		strings.TrimSpace(couponCode),
		plan.Code,
		price,
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

	quote.Calendar = saleCalendar
	quote.DomainID = domainID
	quote.Kind = kind
	quote.PlanAmountCents = configuredTotal
	quote.SubscriptionVersion = sub.ID
	quote.CurrentEntitlementSource = sub.EntitlementSource
	if quote.CurrentEntitlementSource == "" {
		quote.CurrentEntitlementSource = billing.EntitlementStandard
	}
	quote.PriceVersion = version
	quote.Digest = quote.ComputeDigest(key)
	if queries, ok := q.(*store.Queries); ok {
		if h.Pool == nil {
			authError(c, 503, "DB_ERROR")
			return billing.Quote{}, false
		}
		document, documentErr := CaptureInvoiceDocumentSnapshot(c, store.New(h.Pool), tenantID, quote.FinalAmountCents, quote.Currency)
		if documentErr != nil {
			authError(c, 409, "INVOICE_PROFILE_INVALID")
			return billing.Quote{}, false
		}

		snapshot, _ := json.Marshal(newPlanResponse(plan))
		quote.PlanSnapshotDigest, err = billing.SnapshotDigest(snapshot)
		if err != nil {
			authError(c, 500, "QUOTE_ERROR")
			return billing.Quote{}, false
		}
		quote.DocumentSnapshotDigest, err = billing.SnapshotDigest(document)
		if err != nil {
			authError(c, 500, "QUOTE_ERROR")
			return billing.Quote{}, false
		}
		quote.Digest = quote.ComputeDigest(key)
		payload, _ := json.Marshal(quote)
		if saveErr := queries.SaveBillingQuote(c, store.SaveBillingQuoteParams{Digest: quote.Digest, TenantID: tenantID, Payload: payload, PlanSnapshot: snapshot, DocumentSnapshot: document, ExpiresAt: pgtype.Timestamptz{Time: quote.ExpiresAt, Valid: true}}); saveErr != nil {
			authError(c, 500, "DB_ERROR")
			return billing.Quote{}, false
		}
	}
	return quote, true
}

// CheckoutRequest carries either a signed quote (the documented flow) or the
// legacy plan_code body. The legacy shape is preserved for compatibility with
// api/v1-remediation.openapi.yaml: the server builds and signs the quote
// itself, so the tenant never gains a way to bypass pricing (M5 review 3.2).
type CheckoutRequest struct {
	MerchantID   int64          `json:"merchant_id"`
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
	c.Set("billing.merchant_id", req.MerchantID)
	if key := strings.TrimSpace(c.GetHeader("Idempotency-Key")); key != "" {
		if len(key) > 200 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		previous, lookupErr := q.GetCheckoutOrderByIdempotencyKey(c, store.GetCheckoutOrderByIdempotencyKeyParams{TenantID: tenantID, IdempotencyKey: pgtype.Text{String: key, Valid: true}})
		if lookupErr == nil {
			same := false
			if req.Quote != nil {
				same = req.Quote.TenantID == tenantID && req.Quote.VerifySignature(h.quoteKey()) == nil && previous.QuoteDigest.Valid && previous.QuoteDigest.String == req.Quote.Digest
			} else {
				cycle := req.BillingCycle
				if cycle == "" {
					cycle = billing.CycleMonthly
				}
				same = previous.PlanCode.Valid && previous.PlanCode.String == req.PlanCode && previous.BillingCycle == cycle
				redemption, re := q.GetCouponRedemptionForOrder(c, store.GetCouponRedemptionForOrderParams{TenantID: tenantID, OrderID: pgtype.Int8{Int64: previous.ID, Valid: true}})
				if re != nil && !errors.Is(re, pgx.ErrNoRows) {
					authError(c, 500, "DB_ERROR")
					return
				}
				same = same && ((req.CouponCode == "" && errors.Is(re, pgx.ErrNoRows)) || (re == nil && redemption.CouponCode == req.CouponCode))
			}
			if !same {
				authError(c, 409, "ORDER_EXISTS")
				return
			}
			if previous.Status != "pending" && previous.Status != "paid" {
				c.JSON(200, gin.H{"order": previous, "payment_method": "manual"})
				return
			}
			h.finishCheckout(c, previous)
			return
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			authError(c, 500, "DB_ERROR")
			return
		}
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
	if queries, ok := q.(*store.Queries); ok {
		stored, storedErr := queries.GetBillingQuote(c, store.GetBillingQuoteParams{TenantID: tenantID, Digest: quote.Digest})
		if storedErr != nil {
			authError(c, 409, "QUOTE_INVALID")
			return
		}
		if quote.DomainID > 0 {
			domain, domainErr := queries.GetDomainByID(c, store.GetDomainByIDParams{TenantID: tenantID, ID: quote.DomainID})
			if domainErr != nil || domain.Status != "active" || !domain.VerifiedAt.Valid || domain.VerifiedAt.Time.After(time.Now()) {
				authError(c, 409, "DOMAIN_VERIFICATION_REQUIRED")
				return
			}
		}
		var saved billing.Quote
		if json.Unmarshal(stored.Payload, &saved) != nil || saved.Digest != quote.Digest || saved.VerifySignature(h.quoteKey()) != nil || saved.VerifySnapshots(stored.PlanSnapshot, stored.DocumentSnapshot) != nil {
			authError(c, 409, "QUOTE_INVALID")
			return
		}
		fits, fitErr := queries.PlanSnapshotFitsResources(c, store.PlanSnapshotFitsResourcesParams{TenantID: tenantID, PlanSnapshot: stored.PlanSnapshot})
		if fitErr != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
		if !fits {
			authError(c, 409, "PLAN_CAPACITY_EXCEEDED")
			return
		}

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
	if queries, ok := q.(*store.Queries); ok {
		if calendarErr := verifyQuoteBillingCalendar(c.Request.Context(), queries, tenantID, quote.Calendar); calendarErr != nil {
			if errors.Is(calendarErr, errBillingCalendarReview) {
				authError(c, 409, "BILLING_CALENDAR_REVIEW_REQUIRED")
			} else {
				authError(c, 409, "BILLING_CALENDAR_CHANGED")
			}
			return
		}
	}
	_, priceErr := billing.AddCycle(quote.PeriodStart, quote.BillingCycle)
	calendarLimit := quote.PeriodStart.AddDate(1, 0, 0)
	if quote.Calendar != nil {
		anchor, calendarErr := quote.Calendar.Anchor()
		if calendarErr == nil {
			calendarLimit, calendarErr = billing.AddAnchoredCycle(quote.PeriodStart, quote.BillingCycle, anchor)
		}
		if calendarErr != nil {
			priceErr = calendarErr
		}
	}

	discountsExceedBase := quote.DiscountCents > quote.BaseAmountCents || quote.ProratedCreditCents > quote.BaseAmountCents-quote.DiscountCents
	if priceErr != nil || quote.FinalAmountCents < 0 || quote.BaseAmountCents < 0 || quote.DiscountCents < 0 || quote.ProratedCreditCents < 0 || discountsExceedBase || quote.FinalAmountCents != quote.BaseAmountCents-quote.DiscountCents-quote.ProratedCreditCents || !quote.PeriodEnd.After(quote.PeriodStart) || quote.GeneratedAt.After(time.Now().UTC().Add(time.Minute)) || quote.PeriodStart.Before(quote.GeneratedAt.Add(-time.Minute)) || quote.PeriodEnd.After(calendarLimit) {
		authError(c, http.StatusConflict, "QUOTE_INVALID")
		return
	}
	if req.MerchantID == 0 && h.Stripe != nil && quote.FinalAmountCents > 0 {
		if err := h.Stripe.CheckCheckoutAmount(quote.Currency, quote.FinalAmountCents); err != nil {
			authError(c, 409, "PAYMENT_AMOUNT_OUTSIDE_LIMITS")
			return
		}
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
	if !existing {
		current, currentErr := q.GetActiveSubscriptionByTenant(c, tenantID)
		if currentErr != nil && !errors.Is(currentErr, pgx.ErrNoRows) {
			authError(c, 500, "DB_ERROR")
			return
		}
		if !billing.QuoteSourceMatches(quote.CurrentEntitlementSource, current.EntitlementSource) {
			authError(c, 409, "QUOTE_INVALID")
			return
		}
		var channelID pgtype.Int8
		attribution, attributionErr := q.GetChannelAttributionByTenant(c, tenantID)
		if attributionErr == nil {
			channelID = pgtype.Int8{Int64: attribution.ChannelID, Valid: true}
		} else if !errors.Is(attributionErr, pgx.ErrNoRows) {
			authError(c, http.StatusInternalServerError, "DB_ERROR")
			return
		}
		order, err = q.CreateOrder(c, store.CreateOrderParams{TenantID: tenantID, OrderNo: fmt.Sprintf("ORD-%d-%d", tenantID, time.Now().UnixNano()), Kind: quote.Kind, Status: "pending", AmountCents: quote.FinalAmountCents, Currency: quote.Currency, PlanCode: pgtype.Text{String: plan.Code, Valid: true}, IdempotencyKey: idempotency, ChannelID: channelID, BillingCycle: quote.BillingCycle})
		if err != nil {
			authError(c, http.StatusConflict, "ORDER_EXISTS")
			return
		}
	}
	if !existing {
		if queries, ok := q.(*store.Queries); ok {
			if _, bindErr := queries.BindOrderQuote(c, store.BindOrderQuoteParams{TenantID: tenantID, ID: order.ID, QuoteDigest: pgtype.Text{String: quote.Digest, Valid: true}}); bindErr != nil {
				authError(c, 500, "DB_ERROR")
				return
			}
		}
	}
	if !existing {
		order.QuoteDigest = pgtype.Text{String: quote.Digest, Valid: true}
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
	h.finishCheckout(c, order)
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

	receipts := []gin.H{}
	if queries, ok := q.(*store.Queries); ok {
		payments, lookupErr := queries.ListPaymentsByTenant(c, tenantID)
		if lookupErr != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
		for _, receipt := range payments {
			receipts = append(receipts, gin.H{"id": receipt.ID, "order_id": receipt.OrderID, "amount_cents": receipt.AmountCents, "amount_cents_exact": strconv.FormatInt(receipt.AmountCents, 10), "currency": receipt.Currency, "status": receipt.Status, "provider": receipt.Provider, "paid_at": receipt.PaidAt})
		}
	}
	c.JSON(http.StatusOK, gin.H{"orders": orders, "payments": receipts})
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
	if h.Pool != nil {
		intent, lookupErr := store.New(h.Pool).PlatformGetCheckoutIntent(c, store.PlatformGetCheckoutIntentParams{TenantID: tenantID, OrderID: orderID})
		if lookupErr == nil && intent.State != "closed" {
			middleware.AfterCommit(c, func() { h.cancelProviderCheckout(c, tenantID, orderID) })
			return
		}
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			authError(c, 500, "DB_ERROR")
			return
		}
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
func (h *BillingHandler) cancelAbandonedOrder(ctx context.Context, orderID int64) error {
	return h.cancelAbandonedEvent(ctx, payment.WebhookEvent{OrderID: orderID}, nil)
}

func (h *BillingHandler) cancelAbandonedEvent(ctx context.Context, event payment.WebhookEvent, raw []byte, scopes ...string) error {
	scope := "legacy"
	if len(scopes) > 0 && scopes[0] != "" {
		scope = scopes[0]
	}
	provider := event.Provider
	if provider == "" {
		provider = payment.ProviderStripe
	}
	if h.Pool == nil {
		return errors.New("database pool is not configured")
	}
	tenantID, err := store.New(h.Pool).PlatformGetOrderTenantID(ctx, event.OrderID)
	if err != nil {
		return err
	}
	tx, err := h.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(ctx, tenantID); err != nil {
		return err
	}
	intent, intentErr := q.PlatformGetCheckoutIntent(ctx, store.PlatformGetCheckoutIntentParams{TenantID: tenantID, OrderID: event.OrderID})
	if intentErr == nil && (intent.Provider != provider || intent.AccountScope != scope) {
		return ErrPaymentVerificationFailed
	}
	if errors.Is(intentErr, pgx.ErrNoRows) && scope != "legacy" {
		return ErrPaymentVerificationFailed
	}
	if intentErr != nil && !errors.Is(intentErr, pgx.ErrNoRows) {
		return intentErr
	}
	if event.EventID != "" {
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		_, err = q.PlatformInsertPaymentEventPlaceholder(ctx, store.PlatformInsertPaymentEventPlaceholderParams{Provider: provider, AccountScope: scope, ProviderEventID: event.EventID, Payload: raw})
		if errors.Is(err, pgx.ErrNoRows) {
			original, e := q.PlatformGetPaymentEventByProviderAndID(ctx, store.PlatformGetPaymentEventByProviderAndIDParams{Provider: provider, AccountScope: scope, ProviderEventID: event.EventID})
			if e != nil {
				return e
			}
			var left, right any
			if json.Unmarshal(original.Payload, &left) != nil || json.Unmarshal(raw, &right) != nil || !reflect.DeepEqual(left, right) {
				return ErrPaymentVerificationFailed
			}
			return tx.Commit(ctx)
		}
		if err != nil {
			return err
		}
	}
	order, err := q.PlatformGetOrderForSettlement(ctx, store.PlatformGetOrderForSettlementParams{TenantID: tenantID, ID: event.OrderID})
	if err != nil {
		return err
	}
	if order.Status != "pending" {
		return tx.Commit(ctx)
	}
	if _, err = q.PlatformCancelPendingOrder(ctx, event.OrderID); err != nil {
		return err
	}
	redemption, err := q.GetCouponRedemptionForOrder(ctx, store.GetCouponRedemptionForOrderParams{TenantID: tenantID, OrderID: pgtype.Int8{Int64: event.OrderID, Valid: true}})
	if err == nil {
		if _, err = q.PlatformReleaseCouponRedemption(ctx, store.PlatformReleaseCouponRedemptionParams{CouponCode: redemption.CouponCode, OrderID: pgtype.Int8{Int64: event.OrderID, Valid: true}}); err != nil {
			return err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return tx.Commit(ctx)
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
		// (M5 review 3.3). A durable commit is required before acknowledgment.
		if event.OrderID <= 0 {
			authError(c, 400, "INVALID_PAYLOAD")
			return
		}
		if err := h.cancelAbandonedEvent(c.Request.Context(), event, raw); err != nil {
			if errors.Is(err, ErrPaymentVerificationFailed) {
				authError(c, 400, "PAYMENT_VERIFICATION_FAILED")
			} else {
				authError(c, 500, "DB_ERROR")
			}
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "canceled"})
		return
	default:
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}
	// A verified confirmation may carry a renewal settlement instead of an
	// order. Route it through the idempotent renewal settlement path so
	// entitlement restoration happens exactly once; duplicate or out-of-order
	// deliveries are no-ops there.
	routed, routeErr := billingrenew.RouteConfirmedWebhook(c.Request.Context(), h.Pool, billingrenew.ConfirmedRenewalEvent{
		Provider:      payment.ProviderStripe,
		ProviderTxnID: event.ProviderTxnID,
		AmountCents:   event.AmountCents,
		Currency:      event.Currency,
		Raw:           raw,
	})
	if routeErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if routed {
		// Renewal settlements have no order; a renewed subscription already
		// settled idempotently must not fall through to order settlement.
		c.JSON(http.StatusOK, gin.H{"received": true})
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
	h.invoiceDocumentPDF(c)
}

// finishCheckout contacts the provider only after the durable order transaction commits.
func (h *BillingHandler) finishCheckout(c *gin.Context, order store.Orders) {
	middleware.AfterCommit(c, func() {
		if order.Status == "pending" {
			uid, _ := middleware.GetUserID(c)
			if err := h.authorizePersistedOrder(c.Request.Context(), order, uid); err != nil {
				switch {
				case errors.Is(err, errBillingCalendarReview):
					authError(c, 409, "BILLING_CALENDAR_REVIEW_REQUIRED")
				case errors.Is(err, errBillingCalendarChanged):
					authError(c, 409, "BILLING_CALENDAR_CHANGED")
				case errors.Is(err, billing.ErrQuoteTampered):
					authError(c, 409, "QUOTE_INVALID")
				default:
					authError(c, 503, "BILLING_AUTHORITY_UNAVAILABLE")
				}
				return
			}
		}
		if order.AmountCents == 0 && order.Status == "pending" {
			uid, _ := middleware.GetUserID(c)
			if settleErr := SettleOrderWithDetails(c.Request.Context(), h.Pool, order.ID, SettleOptions{Provider: payment.ProviderManual, ActorUserID: uid, AuditAction: "SETTLE order.zero_amount"}); settleErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "ORDER_SETTLE_FAILED"}, "order_id": order.ID})
				return
			}
			order.Status = "paid"
			c.JSON(http.StatusCreated, gin.H{"order": order, "payment_method": "manual"})
			return
		}
		if h.Gateway != nil && order.Status == "pending" {
			merchantID := c.GetInt64("billing.merchant_id")
			modern := merchantID > 0
			if !modern && h.Pool != nil {
				intent, lookupErr := store.New(h.Pool).PlatformGetCheckoutIntent(c, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: order.ID})
				if lookupErr == nil {
					modern = intent.AccountScope != "legacy"
				}
				if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
					authError(c, 500, "DB_ERROR")
					return
				}
			}
			if modern {
				h.Gateway.Checkout(c, order, merchantID)
				return
			}
		}
		if h.Stripe != nil && order.Status == "pending" {
			if h.Pool == nil {
				authError(c, 503, "PAYMENT_PROVIDER_DISABLED")
				return
			}
			queries := store.New(h.Pool)
			_, createErr := queries.PlatformCreateCheckoutIntent(c, store.PlatformCreateCheckoutIntentParams{OrderID: order.ID, TenantID: order.TenantID, Provider: payment.ProviderStripe, AccountScope: "legacy"})
			if errors.Is(createErr, pgx.ErrNoRows) {
				intent, lookupErr := queries.PlatformGetCheckoutIntent(c, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: order.ID})
				if lookupErr != nil {
					authError(c, 500, "DB_ERROR")
					return
				}
				if intent.State == "ready" {
					c.JSON(201, gin.H{"order": order, "payment_method": "stripe", "checkout_url": intent.CheckoutUrl.String, "session_id": intent.SessionID.String})
					return
				}
				c.JSON(http.StatusAccepted, gin.H{"order": order, "payment_method": "stripe", "checkout_state": intent.State})
				return
			}
			if createErr != nil {
				authError(c, 500, "DB_ERROR")
				return
			}
			base := strings.TrimRight(h.BaseURL, "/")
			session, providerErr := h.Stripe.CreateCheckoutSession(c.Request.Context(), order.ID, order.OrderNo, order.AmountCents, order.Currency, base+"/billing?paid=1", base+"/billing?canceled=1")
			if providerErr != nil {
				if markErr := queries.PlatformMarkCheckoutUncertain(c, store.PlatformMarkCheckoutUncertainParams{TenantID: order.TenantID, OrderID: order.ID}); markErr != nil {
					authError(c, 500, "DB_ERROR")
					return
				}
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "PAYMENT_PROVIDER_ERROR"}, "order_id": order.ID})
				return
			}
			saved, saveErr := queries.PlatformSaveCheckoutSession(c, store.PlatformSaveCheckoutSessionParams{TenantID: order.TenantID, OrderID: order.ID, SessionID: pgtype.Text{String: session.ID, Valid: true}, CheckoutUrl: pgtype.Text{String: session.URL, Valid: true}})
			if saveErr != nil {
				authError(c, 500, "DB_ERROR")
				return
			}
			if saved == 0 {
				c.JSON(202, gin.H{"order": order, "checkout_state": "cancel_requested"})
				return
			}
			c.JSON(http.StatusCreated, gin.H{"order": order, "payment_method": "stripe", "checkout_url": session.URL})
			return
		}
		method := "manual"
		if h.Stripe != nil {
			method = "stripe"
		}
		if h.Pool != nil {
			if intent, lookupErr := store.New(h.Pool).PlatformGetCheckoutIntent(c, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: order.ID}); lookupErr == nil {
				method = intent.Provider
			}
		}

		c.JSON(http.StatusCreated, gin.H{"order": order, "payment_method": method})
	})
}

func (h *BillingHandler) cancelProviderCheckout(c *gin.Context, tenantID, orderID int64) {
	q := store.New(h.Pool)
	intent, err := q.PlatformRequestCheckoutCancel(c, store.PlatformRequestCheckoutCancelParams{TenantID: tenantID, OrderID: orderID})
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(202, gin.H{"order_id": orderID, "checkout_state": "requires_review"})
		return
	}
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if intent.AccountScope != "legacy" && h.Gateway != nil {
		if err = h.Gateway.CancelCheckout(c, tenantID, orderID); err != nil {
			authError(c, 502, "PAYMENT_PROVIDER_ERROR")
			return
		}
		c.JSON(202, gin.H{"order_id": orderID, "checkout_state": "cancellation_processed"})
		return
	}
	if h.Stripe == nil || intent.Provider != "stripe" || !intent.SessionID.Valid {
		c.JSON(202, gin.H{"order_id": orderID, "checkout_state": "cancel_requested"})
		return
	}
	status, queryErr := h.Stripe.QueryPayment(c.Request.Context(), "", intent.SessionID.String)
	if queryErr != nil {
		authError(c, 502, "PAYMENT_PROVIDER_ERROR")
		return
	}
	if status.Status == payment.StatusSucceeded {
		if err = SettleOrderWithDetails(c, h.Pool, orderID, SettleOptions{Provider: payment.ProviderStripe, ProviderTxnID: status.ProviderTxnID, AmountCents: status.AmountCents, Currency: status.Currency}); err != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
		c.JSON(409, gin.H{"error": gin.H{"code": "ORDER_ALREADY_PAID"}, "order_id": orderID})
		return
	}
	if status.Status != payment.StatusFailed {
		if err = h.Stripe.CloseCheckout(c.Request.Context(), "", intent.SessionID.String); err != nil {
			authError(c, 502, "PAYMENT_PROVIDER_ERROR")
			return
		}
	}
	if err = h.cancelAbandonedOrder(c, orderID); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if err = q.PlatformMarkCheckoutClosed(c, store.PlatformMarkCheckoutClosedParams{TenantID: tenantID, OrderID: orderID}); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"order_id": orderID, "checkout_state": "closed"})
}

// CancelOrderFromProvider applies a verified merchant-bound terminal event.
func CancelOrderFromProvider(ctx context.Context, pool *db.Pool, event payment.WebhookEvent, scope string) error {
	return (&BillingHandler{Pool: pool}).cancelAbandonedEvent(ctx, event, event.RawPayload, scope)
}

// The legacy gift plan is not a public zero-price offer unless the operator
// explicitly publishes it. Existing gifted tenants retain their entitlement.
func purchasablePlan(plan store.Plans) bool {
	if plan.Code != "free" {
		return true
	}
	var features map[string]any
	return json.Unmarshal(plan.Features, &features) == nil && features["self_service_purchase"] == true
}
