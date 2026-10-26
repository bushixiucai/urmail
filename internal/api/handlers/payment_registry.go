package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

// PaymentGatewayHandler binds production adapters to stable merchant identities and durable intents.
type PaymentGatewayHandler struct {
	Quarantined  bool
	LegacyStripe *payment.Stripe
	Registry     *payment.Registry
	Platform     *store.Queries
	Pool         *db.Pool
	Secrets      SecretsBox
}

// ListMerchants exposes readiness and public configuration, without any credentials.
func (h *PaymentGatewayHandler) ListMerchants(c *gin.Context) {
	rows, err := h.Platform.PlatformListPaymentMerchants(c)
	if supportError(c, err) {
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		merchant, _, loadErr := h.Registry.Load(c, r.ID, false)
		out = append(out, gin.H{"id": r.ID, "provider": r.Provider, "environment": r.Environment, "account_id": r.AccountID, "name": r.Name, "enabled": r.Enabled, "currencies": r.Currencies, "entrypoint": r.Entrypoint, "checkout_limits": json.RawMessage(r.CheckoutLimits), "ready": loadErr == nil && merchant.CheckoutReady(), "adapter_available": r.Provider != payment.ProviderAggregated && r.Provider != "tax_invoice"})
	}
	c.JSON(200, gin.H{"merchants": out})
}

// SaveMerchant never changes a stable identity while rotating credentials.
func (h *PaymentGatewayHandler) SaveMerchant(c *gin.Context) {
	if h.Secrets == nil || h.Registry == nil {
		authError(c, 503, "PAYMENT_PROVIDER_DISABLED")
		return
	}
	var in paymentMerchantInput
	if c.ShouldBindBodyWith(&in, binding.JSON) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	q := requestStore(c, h.Platform)
	proofValue, preflight := c.Get(paymentConfigProofKey)
	proof, _ := proofValue.(paymentConfigProof)
	if preflight && (proof.PayloadHash != paymentConfigBodyHash(c) || proof.Scope != in.Provider+":"+in.Environment+":"+in.AccountID) {
		authError(c, 409, "PAYMENT_STATE_CONFLICT")
		return
	}
	if preflight && proof.Existing {
		id, ok := marketingID(c, "merchant_id")
		if !ok {
			return
		}
		current, e := q.PlatformLockPaymentMerchant(c, id)
		if supportError(c, e) {
			return
		}
		if current.EncryptedConfig != proof.PreviousCipher || !current.UpdatedAt.Time.Equal(proof.PreviousVersion.Time) {
			authError(c, 409, "PAYMENT_STATE_CONFLICT")
			return
		}
	}
	var credentials payment.MerchantSecrets
	if in.Secrets != nil {
		credentials = *in.Secrets
	} else if c.Param("merchant_id") != "" {
		id, ok := marketingID(c, "merchant_id")
		if !ok {
			return
		}
		old, e := q.PlatformGetPaymentMerchant(c, id)
		if supportError(c, e) {
			return
		}
		plain, e := h.Secrets.DecryptString(old.EncryptedConfig)
		if e != nil || json.Unmarshal([]byte(plain), &credentials) != nil {
			authError(c, 503, "PAYMENT_PROVIDER_DISABLED")
			return
		}
	}
	if in.Currencies == nil {
		in.Currencies = []string{}
	}
	m := payment.Merchant{Provider: in.Provider, Environment: in.Environment, AccountID: in.AccountID, Name: in.Name, Enabled: in.Enabled, Currencies: in.Currencies, Entrypoint: in.Entrypoint, CheckoutLimits: in.CheckoutLimits, Secrets: credentials}
	if m.ValidateCheckoutLimits() != nil {
		authError(c, 400, "PAYMENT_CONFIG_INVALID")
		return
	}
	validationErr := m.Validate()
	if validationErr == nil {
		_, validationErr = h.Registry.Build(m)
	}
	if in.Enabled && validationErr == nil && !preflight {
		if _, scoped := middleware.QueriesFromCtx(c); scoped {
			authError(c, 503, "PAYMENT_CONFIG_INVALID")
			return
		}
		provider, e := h.Registry.Build(m)
		if e != nil {
			validationErr = e
		} else {
			validationErr = payment.VerifyProviderIdentity(c.Request.Context(), m, provider)
		}
	}
	if in.Enabled && validationErr != nil {
		authError(c, 400, "PAYMENT_CONFIG_INVALID")
		return
	}
	if !marketingNameValid(in.Name) || in.AccountID == "" || (in.Environment != "live" && in.Environment != "sandbox") || !slices.Contains([]string{"stripe", "wechat", "alipay", "paypal", "aggregated", "tax_invoice"}, in.Provider) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	limits, err := json.Marshal(in.CheckoutLimits)
	if err != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if in.CheckoutLimits == nil {
		limits = []byte(`{}`)
	}
	raw, err := json.Marshal(credentials) //nolint:gosec // Provider credentials are immediately encrypted and are never returned.
	if err != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	encrypted, err := h.Secrets.EncryptString(string(raw))
	if err != nil || !strings.HasPrefix(encrypted, "enc:v1:") {
		authError(c, 503, "PAYMENT_PROVIDER_DISABLED")
		return
	}
	if c.Param("merchant_id") == "" {
		row, e := q.PlatformCreatePaymentMerchant(c, store.PlatformCreatePaymentMerchantParams{Provider: m.Provider, Environment: m.Environment, AccountID: m.AccountID, Name: m.Name, Enabled: m.Enabled, Currencies: m.Currencies, Entrypoint: m.Entrypoint, EncryptedConfig: encrypted, CheckoutLimits: limits})
		if supportError(c, e) {
			return
		}
		c.JSON(201, gin.H{"merchant": row, "ready": validationErr == nil && m.CheckoutReady()})
		return
	}
	id, ok := marketingID(c, "merchant_id")
	if !ok {
		return
	}
	existing, e := q.PlatformGetPaymentMerchant(c, id)
	if supportError(c, e) {
		return
	}
	if existing.Provider != m.Provider || existing.Environment != m.Environment || existing.AccountID != m.AccountID {
		authError(c, 409, "PAYMENT_IDENTITY_IMMUTABLE")
		return
	}
	row, e := q.PlatformUpdatePaymentMerchant(c, store.PlatformUpdatePaymentMerchantParams{ID: id, Name: m.Name, Enabled: m.Enabled, Currencies: m.Currencies, Entrypoint: m.Entrypoint, EncryptedConfig: encrypted, CheckoutLimits: limits})
	if supportError(c, e) {
		return
	}
	c.JSON(200, gin.H{"merchant": row, "ready": validationErr == nil && m.CheckoutReady()})
}

// Methods includes only implemented, configured and explicitly enabled entry points.
func (h *PaymentGatewayHandler) Methods(c *gin.Context) {
	rows, err := h.Platform.PlatformListPaymentMerchants(c)
	if supportError(c, err) {
		return
	}
	out := make([]gin.H, 0)
	currency := strings.ToUpper(c.Query("currency"))
	for _, row := range rows {
		if !row.Enabled || currency != "" && !slices.Contains(row.Currencies, currency) {
			continue
		}
		merchant, _, e := h.Registry.Load(c, row.ID, true)
		if e != nil {
			continue
		}
		limits := map[string]payment.AmountLimit{}
		readyCurrencies := []string{}
		for _, code := range merchant.Currencies {
			if currency != "" && code != currency {
				continue
			}
			if limit, e := merchant.CheckoutLimit(code); e == nil {
				limits[code] = limit
				readyCurrencies = append(readyCurrencies, code)
			}
		}
		if len(readyCurrencies) == 0 {
			continue
		}
		out = append(out, gin.H{"merchant_id": merchant.ID, "provider": merchant.Provider, "name": merchant.Name, "currencies": readyCurrencies, "checkout_limits": limits, "entrypoint": merchant.Entrypoint, "environment": merchant.Environment})
	}
	c.JSON(200, gin.H{"methods": out})
}

// Checkout must be invoked after the tenant order transaction commits.
func (h *PaymentGatewayHandler) Checkout(c *gin.Context, order store.Orders, merchantID int64) {
	if merchantID == 0 {
		intent, e := h.Platform.PlatformGetCheckoutIntent(c, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: order.ID})
		if e != nil || intent.AccountScope == "legacy" {
			authError(c, 409, "PAYMENT_PROVIDER_DISABLED")
			return
		}
		merchant, e := h.Platform.PlatformFindPaymentMerchantByScope(c, store.PlatformFindPaymentMerchantByScopeParams{Provider: intent.Provider, Provider_2: intent.AccountScope})
		if supportError(c, e) {
			return
		}
		merchantID = merchant.ID
	}

	m, p, err := h.Registry.Load(c, merchantID, true)
	if err != nil || !slices.Contains(m.Currencies, order.Currency) {
		authError(c, 409, "PAYMENT_PROVIDER_DISABLED")
		return
	}
	if order.Status != "pending" {
		c.JSON(200, gin.H{"order": order, "checkout_state": order.Status})
		return
	}
	if _, existingErr := h.Platform.PlatformGetCheckoutIntent(c, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: order.ID}); errors.Is(existingErr, pgx.ErrNoRows) {
		if limitErr := m.CheckCheckoutAmount(order.Currency, order.AmountCents); limitErr != nil {
			authError(c, 422, "PAYMENT_AMOUNT_OUTSIDE_LIMITS")
			return
		}
	} else if existingErr != nil {
		supportError(c, existingErr)
		return
	}
	_, err = h.Platform.PlatformCreateCheckoutIntent(c, store.PlatformCreateCheckoutIntentParams{OrderID: order.ID, TenantID: order.TenantID, Provider: m.Provider, AccountScope: m.Scope()})
	if errors.Is(err, pgx.ErrNoRows) {
		intent, e := h.Platform.PlatformGetCheckoutIntent(c, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: order.ID})
		if supportError(c, e) {
			return
		}
		if intent.Provider != m.Provider || intent.AccountScope != m.Scope() {
			authError(c, 409, "PAYMENT_IDENTITY_IMMUTABLE")
			return
		}
		state := intent.State
		if state == "paid" && order.Status == "pending" {
			state = "paid_pending_review"
		}
		response := gin.H{"order": order, "payment_method": m.Provider, "merchant_id": m.ID, "checkout_state": state, "restore_hold": intent.RestoreHold}
		if intent.State == "ready" && !intent.RestoreHold {
			response["checkout_url"] = intent.CheckoutUrl.String
			response["qr_code"] = intent.QrCode.String
			response["session_id"] = intent.SessionID.String
		}
		c.JSON(200, response)
		return
	}
	if supportError(c, err) {
		return
	}
	base := strings.TrimRight(h.Registry.BaseURL, "/")
	result, err := p.CreateCheckout(c.Request.Context(), payment.CheckoutParams{TenantID: order.TenantID, OrderID: order.ID, OrderNo: order.OrderNo, AmountCents: order.AmountCents, Currency: order.Currency, Description: "urMail " + order.OrderNo, SuccessURL: base + "/billing?paid=1", CancelURL: base + "/billing?canceled=1"})
	if err != nil {
		_ = h.Platform.PlatformMarkCheckoutUncertain(c, store.PlatformMarkCheckoutUncertainParams{TenantID: order.TenantID, OrderID: order.ID})
		authError(c, 502, "PAYMENT_PROVIDER_ERROR")
		return
	}
	if err = payment.ValidateCheckoutDestination(m.Provider, result); err != nil {
		_ = h.Platform.PlatformMarkCheckoutUncertain(c, store.PlatformMarkCheckoutUncertainParams{TenantID: order.TenantID, OrderID: order.ID})
		authError(c, 502, "PAYMENT_PROVIDER_ERROR")
		return
	}
	saved, e := h.Platform.PlatformSaveMerchantCheckout(c, store.PlatformSaveMerchantCheckoutParams{TenantID: order.TenantID, OrderID: order.ID, SessionID: pgtype.Text{String: result.SessionID, Valid: true}, CheckoutUrl: pgtype.Text{String: result.URL, Valid: result.URL != ""}, QrCode: pgtype.Text{String: result.QRCode, Valid: result.QRCode != ""}})
	if supportError(c, e) {
		return
	}
	if saved == 0 {
		authError(c, 409, "PAYMENT_STATE_CONFLICT")
		return
	}
	savedIntent, e := h.Platform.PlatformGetCheckoutIntent(c, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: order.ID})
	if supportError(c, e) {
		return
	}
	if savedIntent.State == "cancel_requested" {
		_ = h.CancelCheckout(c.Request.Context(), order.TenantID, order.ID)
		c.JSON(202, gin.H{"order": order, "checkout_state": "cancel_requested"})
		return
	}
	c.JSON(201, gin.H{"order": order, "payment_method": m.Provider, "merchant_id": m.ID, "checkout_url": result.URL, "qr_code": result.QRCode, "session_id": result.SessionID, "checkout_state": "ready"})
}
func canonicalPaymentPayload(provider string, raw []byte) []byte {
	var value any
	if json.Unmarshal(raw, &value) == nil {
		out, _ := json.Marshal(value)
		return out
	}
	if provider == payment.ProviderAlipay {
		values, err := url.ParseQuery(string(raw))
		if err == nil {
			out, _ := json.Marshal(values)
			return out
		}
	}
	out, _ := json.Marshal(map[string][]byte{"raw": raw})
	return out
}

// Webhook verifies first, then persists and claims before any capture side effect.
func (h *PaymentGatewayHandler) Webhook(c *gin.Context) {
	if h.Pool == nil || h.Platform == nil || h.Registry == nil {
		authError(c, 503, "PAYMENT_PROVIDER_DISABLED")
		return
	}
	id, ok := marketingID(c, "merchant_id")
	if !ok {
		return
	}
	m, p, err := h.Registry.Load(c, id, false)
	if err != nil {
		authError(c, 503, "PAYMENT_PROVIDER_DISABLED")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		authError(c, 400, "INVALID_PAYLOAD")
		return
	}
	event, err := p.ParseAndVerifyWebhook(c.Request.Context(), c.Request.Header, raw)
	if err != nil {
		if errors.Is(err, payment.ErrPaymentNotPaid) {
			ackMerchantWebhook(c, m.Provider, "pending")
			return
		}
		var syntax *json.SyntaxError
		var typed *json.UnmarshalTypeError
		permanent := errors.Is(err, payment.ErrInvalidSignature) || errors.Is(err, payment.ErrSignatureExpired) || errors.Is(err, payment.ErrMerchantMismatch) || errors.Is(err, payment.ErrInvalidPayload) || errors.As(err, &syntax) || errors.As(err, &typed)
		if !permanent {
			authError(c, 503, "PAYMENT_PROVIDER_ERROR")
			return
		}
		payment.RecordRejectedWebhook(c, h.Platform, id, "verification_rejected", raw)
		authError(c, 400, "PAYMENT_VERIFICATION_FAILED")
		return
	}
	if err = payment.ValidateMerchantEvent(m, raw, event); err != nil {
		payment.RecordRejectedWebhook(c, h.Platform, id, "merchant_rejected", raw)
		authError(c, 400, "PAYMENT_VERIFICATION_FAILED")
		return
	}
	if event.EventID == "" {
		authError(c, 400, "INVALID_PAYLOAD")
		return
	}
	payload := canonicalPaymentPayload(m.Provider, raw)
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	existing, err := h.Platform.PlatformRecordMerchantWebhook(c, store.PlatformRecordMerchantWebhookParams{MerchantID: id, EventID: event.EventID, PayloadHash: hash, Payload: payload})
	if supportError(c, err) {
		return
	}
	if existing.PayloadHash != hash {
		authError(c, 409, "PAYMENT_EVENT_CONFLICT")
		return
	}
	if existing.State == "completed" {
		ackMerchantWebhook(c, m.Provider, "duplicate")
		return
	}
	if existing.State == "uncertain" || existing.State == "processing" {
		authError(c, 503, "PAYMENT_PENDING_REVIEW")
		return
	}
	if existing.State == "rejected" {
		authError(c, 400, "PAYMENT_VERIFICATION_FAILED")
		return
	}
	_, err = h.Platform.PlatformClaimMerchantWebhook(c, store.PlatformClaimMerchantWebhookParams{MerchantID: id, EventID: event.EventID, PayloadHash: hash})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 503, "PAYMENT_PENDING_REVIEW")
		return
	}
	if supportError(c, err) {
		return
	}
	state, code := "completed", "ignored"
	switch {
	case event.EventType == "checkout.session.expired" || event.EventType == "checkout.session.async_payment_failed":
		err = CancelOrderFromProvider(c, h.Pool, event, m.Scope())
		code = "canceled"
		if err != nil {
			state = "uncertain"
			code = "cancel_pending"
		}
	case event.Status == payment.StatusRefunded:
		state = "uncertain"
		code = "refund_reconciliation_required"
		err = payment.ErrUnsupportedOperation
	case event.Status == payment.StatusSucceeded || event.EventType == "CHECKOUT.ORDER.APPROVED":
		state, code, err = h.processVerifiedEvent(c.Request.Context(), m, p, event, payload)
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, finishErr := h.Platform.PlatformFinishMerchantWebhook(finishCtx, store.PlatformFinishMerchantWebhookParams{MerchantID: id, EventID: event.EventID, State: state, ResultCode: pgtype.Text{String: code, Valid: true}})
	if finishErr != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if err != nil {
		if state == "rejected" {
			authError(c, 400, "PAYMENT_VERIFICATION_FAILED")
		} else {
			authError(c, 503, "PAYMENT_PENDING_REVIEW")
		}
		return
	}
	ackMerchantWebhook(c, m.Provider, code)
}
func (h *PaymentGatewayHandler) processVerifiedEvent(ctx context.Context, m payment.Merchant, p payment.Provider, event payment.WebhookEvent, payload []byte) (string, string, error) {
	verificationFailureState := "rejected"
	if event.Status == payment.StatusSucceeded {
		verificationFailureState = "uncertain"
	}
	order, err := h.Platform.PlatformGetMerchantOrderByReference(ctx, store.PlatformGetMerchantOrderByReferenceParams{OrderID: event.OrderID, OrderNo: event.OrderNo})
	var externalPayPalOrder string
	if m.Provider == payment.ProviderPayPal {
		var resource struct {
			Resource struct {
				Supplementary struct {
					Related struct {
						OrderID string `json:"order_id"`
					} `json:"related_ids"`
				} `json:"supplementary_data"`
			} `json:"resource"`
		}
		if json.Unmarshal(payload, &resource) == nil {
			externalPayPalOrder = resource.Resource.Supplementary.Related.OrderID
		}
	}
	if err != nil && event.OrderID == 0 && externalPayPalOrder != "" {
		order, err = h.Platform.PlatformGetMerchantOrderBySession(ctx, store.PlatformGetMerchantOrderBySessionParams{Provider: m.Provider, AccountScope: m.Scope(), SessionID: externalPayPalOrder})
		if err == nil && event.OrderNo == externalPayPalOrder {
			event.OrderNo = order.OrderNo
		}
	}
	if err != nil {
		if event.Status == payment.StatusSucceeded {
			return "uncertain", "unmatched_receipt", err
		}
		return "rejected", "order_not_found", err
	}
	if event.OrderNo != "" && event.OrderNo != order.OrderNo {
		return verificationFailureState, "order_mismatch", payment.ErrMerchantMismatch
	}
	intent, err := h.Platform.PlatformGetCheckoutIntent(ctx, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: order.ID})
	if err != nil || intent.Provider != m.Provider || intent.AccountScope != m.Scope() {
		return verificationFailureState, "intent_mismatch", payment.ErrMerchantMismatch
	}
	if externalPayPalOrder != "" && intent.SessionID.String != externalPayPalOrder {
		return "uncertain", "session_mismatch", payment.ErrMerchantMismatch
	}
	if event.EventType == "CHECKOUT.ORDER.APPROVED" {
		if intent.RestoreHold {
			if flagErr := h.Platform.PlatformFlagCheckoutReview(ctx, store.PlatformFlagCheckoutReviewParams{TenantID: order.TenantID, OrderID: order.ID}); flagErr != nil {
				return "uncertain", "restore_review_alert_pending", flagErr
			}
			return "uncertain", "restore_manual_decision_required", payment.ErrUnsupportedOperation
		}
		if event.AmountCents != order.AmountCents || strings.ToUpper(event.Currency) != order.Currency {
			return "rejected", "approval_amount_mismatch", payment.ErrAmountMismatch
		}
		if intent.SessionID.String != event.ProviderTxnID {
			return "rejected", "session_mismatch", payment.ErrMerchantMismatch
		}
		if order.Status != "pending" {
			return "completed", "approval_not_captured", nil
		}
		pp, ok := p.(*payment.PayPal)
		if !ok {
			return "rejected", "provider_mismatch", payment.ErrMerchantMismatch
		}
		result, captureErr := h.capturePayPalOnce(ctx, m, pp, order, event)
		if captureErr != nil {
			if errors.Is(captureErr, errCaptureInProgress) {
				return "received", "capture_in_progress", captureErr
			}
			return "uncertain", "capture_unknown", captureErr
		}
		event.ProviderTxnID = result.ProviderTxnID
		event.AmountCents = result.AmountCents
		event.Currency = result.Currency
		event.Status = payment.StatusSucceeded
	}
	err = SettleOrderWithDetails(ctx, h.Pool, order.ID, SettleOptions{Provider: m.Provider, AccountScope: m.Scope(), ProviderTxnID: event.ProviderTxnID, EventID: event.EventID, AmountCents: event.AmountCents, Currency: event.Currency, RawPayload: payload})
	if errors.Is(err, ErrPaymentVerificationFailed) || errors.Is(err, ErrOrderNotFound) {
		return "uncertain", "receipt_verification_pending", err
	}
	if err != nil {
		return "uncertain", "settlement_pending", err
	}
	return "completed", "settled", nil
}

// Query queries a durable intent and reconciles confirmed receipts without creating a payment.
func (h *PaymentGatewayHandler) Query(c *gin.Context) {
	id, ok := marketingID(c, "merchant_id")
	if !ok {
		return
	}
	orderID, ok := marketingID(c, "order_id")
	if !ok {
		return
	}
	m, p, err := h.Registry.Load(c, id, false)
	if err != nil {
		authError(c, 503, "PAYMENT_PROVIDER_DISABLED")
		return
	}
	order, err := h.Platform.PlatformGetMerchantOrderByReference(c, store.PlatformGetMerchantOrderByReferenceParams{OrderID: orderID})
	if supportError(c, err) {
		return
	}
	intent, err := h.Platform.PlatformGetCheckoutIntent(c, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: order.ID})
	if supportError(c, err) {
		return
	}
	if intent.Provider != m.Provider || intent.AccountScope != m.Scope() {
		authError(c, 409, "PAYMENT_IDENTITY_IMMUTABLE")
		return
	}
	status, err := p.QueryPayment(c.Request.Context(), order.OrderNo, intent.SessionID.String)
	if err != nil {
		authError(c, 502, "PAYMENT_PROVIDER_ERROR")
		return
	}
	switch status.Status {
	case payment.StatusSucceeded:
		payload, _ := json.Marshal(gin.H{"transaction_id": status.ProviderTxnID, "status": status.Status, "amount_cents": status.AmountCents, "currency": status.Currency})
		actor, _ := middleware.GetUserID(c)
		middleware.SetPlatformAuditTenant(c, order.TenantID)
		err = SettleOrderWithDetails(c.Request.Context(), h.Pool, order.ID, SettleOptions{ActorUserID: actor, AuditAction: "READ order.provider_query", ReceiptOnly: h.Quarantined || intent.RestoreHold, Provider: m.Provider, AccountScope: m.Scope(), ProviderTxnID: status.ProviderTxnID, EventID: "query:" + status.ProviderTxnID + ":" + strconv.FormatInt(status.AmountCents, 10), AmountCents: status.AmountCents, Currency: status.Currency, RawPayload: payload})
		if supportError(c, err) {
			return
		}
	case payment.StatusFailed:
		if supportError(c, h.confirmCheckoutClosed(c.Request.Context(), intent)) {
			return
		}
		intent, err = h.Platform.PlatformGetCheckoutIntent(c, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: order.ID})
		if supportError(c, err) {
			return
		}
	}
	c.JSON(200, gin.H{"status": status, "receipt_only": h.Quarantined || intent.RestoreHold, "restore_hold": intent.RestoreHold})
}

// Recover converts abandoned capture leases to an explicit manual-review state.
func (h *PaymentGatewayHandler) Recover(ctx context.Context) error {
	if _, err := h.Platform.PlatformExpireMerchantWebhookClaims(ctx); err != nil {
		return err
	}
	rows, err := h.Platform.PlatformListMerchantCheckoutsToRecover(ctx)
	if err != nil {
		return err
	}
	for _, intent := range rows {
		var recoveryErr error
		if intent.AccountScope == "legacy" {
			_, recoveryErr = h.reconcileLegacyCheckout(ctx, intent, 0, false, true)
		} else {
			recoveryErr = h.recoverCheckout(ctx, intent)
		}
		if recoveryErr != nil {
			if err = h.Platform.PlatformFlagCheckoutReview(ctx, store.PlatformFlagCheckoutReviewParams{OrderID: intent.OrderID, TenantID: intent.TenantID}); err != nil {
				return err
			}
		}
		if err = h.Platform.PlatformTouchMerchantCheckout(ctx, store.PlatformTouchMerchantCheckoutParams{TenantID: intent.TenantID, OrderID: intent.OrderID}); err != nil {
			return err
		}
	}
	return nil
}

// CancelCheckout queries before closing; a provider without close capability remains pending reconciliation.
func (h *PaymentGatewayHandler) CancelCheckout(ctx context.Context, tenantID, orderID int64) error {
	intent, err := h.Platform.PlatformGetCheckoutIntent(ctx, store.PlatformGetCheckoutIntentParams{TenantID: tenantID, OrderID: orderID})
	if err != nil {
		return err
	}
	row, err := h.Platform.PlatformFindPaymentMerchantByScope(ctx, store.PlatformFindPaymentMerchantByScopeParams{Provider: intent.Provider, Provider_2: intent.AccountScope})
	if err != nil {
		return err
	}
	m, p, err := h.Registry.Load(ctx, row.ID, false)
	if err != nil {
		return err
	}
	order, err := h.Platform.PlatformGetMerchantOrderByReference(ctx, store.PlatformGetMerchantOrderByReferenceParams{OrderID: orderID})
	if err != nil {
		return err
	}
	status, err := p.QueryPayment(ctx, order.OrderNo, intent.SessionID.String)
	if err != nil {
		return err
	}
	if status.Status == payment.StatusSucceeded {
		payload, _ := json.Marshal(gin.H{"transaction_id": status.ProviderTxnID, "status": status.Status, "amount_cents": status.AmountCents, "currency": status.Currency})
		return SettleOrderWithDetails(ctx, h.Pool, orderID, SettleOptions{Provider: m.Provider, AccountScope: m.Scope(), ProviderTxnID: status.ProviderTxnID, EventID: "cancel-query:" + status.ProviderTxnID, AmountCents: status.AmountCents, Currency: status.Currency, RawPayload: payload})
	}
	if status.Status != payment.StatusFailed {
		closer, ok := p.(payment.CloseCheckoutProvider)
		if !ok {
			return payment.ErrUnsupportedOperation
		}
		if err = closer.CloseCheckout(ctx, order.OrderNo, intent.SessionID.String); err != nil {
			return err
		}
	}
	return h.confirmCheckoutClosed(ctx, intent)
}

// confirmCheckoutClosed persists a provider-confirmed terminal fact without a new external write.
func (h *PaymentGatewayHandler) confirmCheckoutClosed(ctx context.Context, intent store.PaymentCheckoutIntents) error {
	raw, _ := json.Marshal(gin.H{"order_id": intent.OrderID, "session_id": intent.SessionID.String, "result": "closed"})
	if err := CancelOrderFromProvider(ctx, h.Pool, payment.WebhookEvent{Provider: intent.Provider, OrderID: intent.OrderID, EventID: "confirmed-close:" + strconv.FormatInt(intent.OrderID, 10), RawPayload: raw}, intent.AccountScope); err != nil {
		return err
	}
	return h.Platform.PlatformMarkCheckoutClosed(ctx, store.PlatformMarkCheckoutClosedParams{TenantID: intent.TenantID, OrderID: intent.OrderID})
}

func (h *PaymentGatewayHandler) recoverCheckout(ctx context.Context, intent store.PaymentCheckoutIntents) error {
	row, err := h.Platform.PlatformFindPaymentMerchantByScope(ctx, store.PlatformFindPaymentMerchantByScopeParams{Provider: intent.Provider, Provider_2: intent.AccountScope})
	if err != nil {
		return err
	}
	m, p, err := h.Registry.Load(ctx, row.ID, false)
	if err != nil {
		return err
	}
	order, err := h.Platform.PlatformGetMerchantOrderByReference(ctx, store.PlatformGetMerchantOrderByReferenceParams{OrderID: intent.OrderID})
	if err != nil {
		return err
	}
	if intent.State == "cancel_requested" {
		if err = h.CancelCheckout(ctx, intent.TenantID, intent.OrderID); err != nil {
			return err
		}
		return h.Platform.PlatformResolveCheckoutReview(ctx, intent.OrderID)
	}
	if intent.SessionID.String == "" && (m.Provider == payment.ProviderStripe || m.Provider == payment.ProviderPayPal) {
		return errors.New("checkout session requires merchant investigation")
	}
	result, err := p.QueryPayment(ctx, order.OrderNo, intent.SessionID.String)
	if err != nil {
		return err
	}
	if result.Status == payment.StatusSucceeded {
		payload, _ := json.Marshal(gin.H{"transaction_id": result.ProviderTxnID, "amount_cents": result.AmountCents, "currency": result.Currency})
		if err = SettleOrderWithDetails(ctx, h.Pool, order.ID, SettleOptions{Provider: m.Provider, AccountScope: m.Scope(), ProviderTxnID: result.ProviderTxnID, EventID: "recovery-query:" + result.ProviderTxnID, AmountCents: result.AmountCents, Currency: result.Currency, RawPayload: payload}); err != nil {
			return err
		}
		return h.Platform.PlatformResolveCheckoutReview(ctx, order.ID)
	}
	if result.Status == payment.StatusFailed {
		if err = h.CancelCheckout(ctx, intent.TenantID, intent.OrderID); err != nil {
			return err
		}
		return h.Platform.PlatformResolveCheckoutReview(ctx, order.ID)
	}

	if intent.State == "ready" {
		return nil
	}
	return errors.New("checkout remains unresolved")
}

func ackMerchantWebhook(c *gin.Context, provider, status string) {
	if provider == payment.ProviderAlipay {
		c.String(200, "success")
		return
	}
	if provider == payment.ProviderWeChat {
		c.JSON(200, gin.H{"code": "SUCCESS", "message": "OK"})
		return
	}
	c.JSON(200, gin.H{"status": status})
}

var errCaptureInProgress = errors.New("payment capture is already being processed")

func (h *PaymentGatewayHandler) capturePayPalOnce(ctx context.Context, m payment.Merchant, p *payment.PayPal, order store.Orders, event payment.WebhookEvent) (payment.StatusResult, error) {
	eventID := "capture:" + event.ProviderTxnID
	payload, _ := json.Marshal(gin.H{"order_id": order.ID, "session_id": event.ProviderTxnID, "amount_cents": event.AmountCents, "currency": event.Currency})
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	claim, err := h.Platform.PlatformRecordMerchantWebhook(ctx, store.PlatformRecordMerchantWebhookParams{MerchantID: m.ID, EventID: eventID, PayloadHash: hash, Payload: payload})
	if err != nil {
		return payment.StatusResult{}, err
	}
	if claim.PayloadHash != hash {
		return payment.StatusResult{}, payment.ErrMerchantMismatch
	}
	if claim.State != "received" {
		status, e := p.QueryPayment(ctx, order.OrderNo, event.ProviderTxnID)
		if e != nil {
			return payment.StatusResult{}, e
		}
		if status.Status == payment.StatusSucceeded {
			return status, nil
		}
		if claim.State == "processing" {
			return payment.StatusResult{}, errCaptureInProgress
		}
		return payment.StatusResult{}, payment.ErrPaymentNotPaid
	}
	_, err = h.Platform.PlatformClaimMerchantWebhook(ctx, store.PlatformClaimMerchantWebhookParams{MerchantID: m.ID, EventID: eventID, PayloadHash: hash})
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.StatusResult{}, errCaptureInProgress
	}
	if err != nil {
		return payment.StatusResult{}, err
	}
	capture, err := p.CaptureOrder(ctx, event.ProviderTxnID)
	result := payment.StatusResult{}
	if err == nil {
		if !strings.EqualFold(capture.Status, "COMPLETED") || capture.CaptureID == "" {
			err = payment.ErrPaymentNotPaid
		} else {
			amount, e := payment.ParseDecimalAmount(capture.AmountValue, capture.Currency)
			if e != nil {
				err = e
			} else {
				result = payment.StatusResult{ProviderTxnID: capture.CaptureID, Status: payment.StatusSucceeded, AmountCents: amount, Currency: capture.Currency}
			}
		}
	}
	state, code := "completed", "capture_confirmed"
	if err != nil {
		state = "uncertain"
		code = "capture_unknown"
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, finishErr := h.Platform.PlatformFinishMerchantWebhook(finishCtx, store.PlatformFinishMerchantWebhookParams{MerchantID: m.ID, EventID: eventID, State: state, ResultCode: pgtype.Text{String: code, Valid: true}})
	if finishErr != nil {
		return payment.StatusResult{}, finishErr
	}
	return result, err
}
