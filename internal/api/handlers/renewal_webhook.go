package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/billingrenew"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

// recurringWebhook only reconciles an existing financial intent. Retrying this
// path cannot create, confirm or capture a provider payment.
func (h *PaymentGatewayHandler) recurringWebhook(c *gin.Context, merchant payment.Merchant, provider payment.Provider, event payment.WebhookEvent, payload []byte, receipt store.PaymentWebhookInbox) {
	if receipt.State == "completed" {
		ackMerchantWebhook(c, merchant.Provider, "duplicate")
		return
	}
	if receipt.State == "rejected" {
		authError(c, 400, "PAYMENT_VERIFICATION_FAILED")
		return
	}
	claim, err := h.Platform.PlatformClaimRecurringWebhook(c.Request.Context(), store.PlatformClaimRecurringWebhookParams{MerchantID: merchant.ID, EventID: event.EventID, PayloadHash: receipt.PayloadHash})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 503, "PAYMENT_PENDING_REVIEW")
		return
	}
	if supportError(c, err) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 40*time.Second)
	state, code, workErr := h.reconcileRecurringReceipt(ctx, merchant, provider, event, payload)
	cancel()
	finishctx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finishCancel()
	changed, finishErr := h.Platform.PlatformFinishRecurringWebhook(finishctx, store.PlatformFinishRecurringWebhookParams{MerchantID: merchant.ID, EventID: event.EventID, PayloadHash: receipt.PayloadHash, ProcessingUntil: claim.ProcessingUntil, State: state, ResultCode: pgtype.Text{String: code, Valid: true}})
	if finishErr != nil || changed != 1 {
		authError(c, 503, "PAYMENT_PENDING_REVIEW")
		return
	}
	if workErr != nil {
		if state == "rejected" {
			authError(c, 400, "PAYMENT_VERIFICATION_FAILED")
		} else {
			authError(c, 503, "PAYMENT_PENDING_REVIEW")
		}
		return
	}
	ackMerchantWebhook(c, merchant.Provider, code)
}

func (h *PaymentGatewayHandler) reconcileRecurringReceipt(ctx context.Context, merchant payment.Merchant, provider payment.Provider, event payment.WebhookEvent, payload []byte) (string, string, error) {
	stripe, ok := provider.(*payment.Stripe)
	if !ok || event.Status != payment.StatusSucceeded {
		return "rejected", "renewal_provider_mismatch", payment.ErrMerchantMismatch
	}
	key := billingrenew.RenewalDedupeKeyFromPayload(payload)
	attempt, err := h.Platform.PlatformFindRenewalAttemptByDedupeKey(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "uncertain", "renewal_intent_missing", payment.ErrInvalidPayload
	}
	if err != nil {
		return "uncertain", "renewal_intent_unavailable", err
	}
	if attempt.MerchantScope != merchant.Scope() || !attempt.MerchantID.Valid || attempt.MerchantID.Int64 != merchant.ID {
		return "rejected", "renewal_account_mismatch", payment.ErrMerchantMismatch
	}
	if !attempt.SideEffectIntentAt.Valid || attempt.State == "canceled" {
		return "uncertain", "renewal_unexpected_receipt_review", payment.ErrInvalidPayload
	}
	if err = payment.VerifyProviderIdentity(ctx, merchant, provider); err != nil {
		return "uncertain", "renewal_account_unverified", err
	}
	actual, err := stripe.QueryRecurringReceipt(ctx, event.ProviderTxnID)
	if err != nil {
		return "uncertain", "renewal_query_pending", err
	}
	if actual.IdempotencyKey != key || actual.TenantID != attempt.TenantID || actual.SubscriptionID != attempt.SubscriptionID || actual.LiveMode != (merchant.Environment == "live") || actual.AmountCents != event.AmountCents || actual.Currency != event.Currency {
		return "uncertain", "renewal_receipt_mismatch", payment.ErrAmountMismatch
	}
	_, err = billingrenew.PlatformSettleRenewalAttempt(ctx, h.Pool, billingrenew.RenewalSettlementParams{TenantID: attempt.TenantID, AttemptID: attempt.ID, Provider: merchant.Provider, AccountScope: merchant.Scope(), ProviderTxnID: actual.ProviderTxnID, AmountCents: actual.AmountCents, Currency: actual.Currency, DedupeKey: key})
	if err != nil {
		return "uncertain", "renewal_settlement_pending", err
	}
	return "completed", "renewal_settled", nil
}
