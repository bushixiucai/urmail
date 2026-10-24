package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

// QueryLegacy imports only verified facts for an existing legacy Stripe intent.
func (h *PaymentGatewayHandler) QueryLegacy(c *gin.Context) {
	id, ok := marketingID(c, "id")
	if !ok {
		return
	}
	actor, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	order, err := h.Platform.PlatformGetMerchantOrderByReference(c, store.PlatformGetMerchantOrderByReferenceParams{OrderID: id})
	if supportError(c, err) {
		return
	}
	intent, err := h.Platform.PlatformGetCheckoutIntent(c, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: id})
	if supportError(c, err) {
		return
	}
	if intent.AccountScope != "legacy" || intent.Provider != payment.ProviderStripe {
		authError(c, 409, "PAYMENT_IDENTITY_IMMUTABLE")
		return
	}
	middleware.SetPlatformAuditTenant(c, order.TenantID)
	result, err := h.reconcileLegacyCheckout(c.Request.Context(), intent, actor, h.Quarantined || intent.RestoreHold, false)
	if err != nil {
		_ = h.Platform.PlatformFlagCheckoutReview(c, store.PlatformFlagCheckoutReviewParams{OrderID: id, TenantID: order.TenantID})
		authError(c, 409, "PAYMENT_RECONCILIATION_REQUIRED")
		return
	}
	intent, err = h.Platform.PlatformGetCheckoutIntent(c, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: id})
	if supportError(c, err) {
		return
	}
	c.JSON(200, gin.H{"status": result, "receipt_only": h.Quarantined || intent.RestoreHold, "restore_hold": intent.RestoreHold, "provider_query_only": true})
}

func (h *PaymentGatewayHandler) reconcileLegacyCheckout(ctx context.Context, intent store.PaymentCheckoutIntents, actor int64, receiptOnly, allowClose bool) (payment.StatusResult, error) {
	if h.LegacyStripe == nil || intent.Provider != payment.ProviderStripe || intent.AccountScope != "legacy" || intent.SessionID.String == "" {
		return payment.StatusResult{}, errors.New("legacy checkout requires original provider session investigation")
	}
	if intent.RestoreHold {
		receiptOnly = true
	}
	order, err := h.Platform.PlatformGetMerchantOrderByReference(ctx, store.PlatformGetMerchantOrderByReferenceParams{OrderID: intent.OrderID})
	if err != nil {
		return payment.StatusResult{}, err
	}
	result, err := h.LegacyStripe.QueryPayment(ctx, order.OrderNo, intent.SessionID.String)
	if err != nil {
		return result, err
	}
	if result.Status == payment.StatusSucceeded {
		payload, _ := json.Marshal(gin.H{"transaction_id": result.ProviderTxnID, "amount_cents": result.AmountCents, "currency": result.Currency})
		action := ""
		if actor > 0 {
			action = "READ order.legacy_provider_query"
		}
		err = SettleOrderWithDetails(ctx, h.Pool, order.ID, SettleOptions{ActorUserID: actor, AuditAction: action, ReceiptOnly: receiptOnly, Provider: payment.ProviderStripe, AccountScope: "legacy", ProviderTxnID: result.ProviderTxnID, EventID: "legacy-query:" + result.ProviderTxnID + ":" + strconv.FormatInt(result.AmountCents, 10), AmountCents: result.AmountCents, Currency: result.Currency, RawPayload: payload})
		if err == nil {
			err = h.Platform.PlatformResolveCheckoutReview(ctx, order.ID)
		}
		return result, err
	}
	if result.Status == payment.StatusFailed {
		if err = h.confirmCheckoutClosed(ctx, intent); err != nil {
			return result, err
		}
		return result, h.Platform.PlatformResolveCheckoutReview(ctx, order.ID)
	}
	// Manual reconciliation, including quarantine, never closes or captures remotely.
	if !allowClose {
		return result, nil
	}
	closed := result.Status == payment.StatusFailed
	if !closed && intent.State == "cancel_requested" {
		if err = h.LegacyStripe.CloseCheckout(ctx, order.OrderNo, intent.SessionID.String); err != nil {
			return result, err
		}
		closed = true
	}
	if closed {
		payload, _ := json.Marshal(gin.H{"order_id": order.ID, "session_id": intent.SessionID.String, "result": "closed"})
		if err = CancelOrderFromProvider(ctx, h.Pool, payment.WebhookEvent{Provider: payment.ProviderStripe, OrderID: order.ID, EventID: "legacy-close:" + order.OrderNo, RawPayload: payload}, "legacy"); err != nil {
			return result, err
		}
		if err = h.Platform.PlatformMarkCheckoutClosed(ctx, store.PlatformMarkCheckoutClosedParams{TenantID: order.TenantID, OrderID: order.ID}); err != nil {
			return result, err
		}
		return result, h.Platform.PlatformResolveCheckoutReview(ctx, order.ID)
	}
	if intent.State != "ready" {
		return result, errors.New("legacy checkout remains unresolved")
	}
	return result, nil
}
