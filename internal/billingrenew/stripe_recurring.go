package billingrenew

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/payment"
)

// StripeAdapterKey identifies the separately approved off-session capability.
const StripeAdapterKey = "stripe_off_session_v1"
const stripePageBudget = 3

var stripeResourceID = regexp.MustCompile(`^[a-z]+_[A-Za-z0-9_]{1,200}$`)
var stripeNonce = regexp.MustCompile(`^[A-Za-z0-9_-]{16,200}$`)
var stripeLastFour = regexp.MustCompile(`^[0-9]{4}$`)
var stripeBrand = regexp.MustCompile(`^[a-z_]{1,30}$`)
var errStripeObservation = errors.New("stripe recurring observation unavailable or inconsistent")

// StripeRecurring uses current credentials for the frozen merchant account.
// Endpoint selection is fixed; Registry.Client only supplies transport policy.
type StripeRecurring struct{ registry *payment.Registry }

// NewProductionAdapters registers implemented adapters without approving accounts.
func NewProductionAdapters(registry *payment.Registry) map[string]RecurringAdapter {
	return map[string]RecurringAdapter{StripeAdapterKey: NewStripeRecurring(registry)}
}

// NewStripeRecurring constructs an adapter without enabling or contacting a merchant.
func NewStripeRecurring(registry *payment.Registry) *StripeRecurring {
	return &StripeRecurring{registry: registry}
}

// Name matches the payment merchant provider, independently of capability approval.
func (*StripeRecurring) Name() string { return payment.ProviderStripe }

func stripeID(id, prefix string) bool {
	return strings.HasPrefix(id, prefix+"_") && stripeResourceID.MatchString(id)
}
func stripeLive(m payment.Merchant, live *bool) bool {
	return live != nil && *live == (m.Environment == "live")
}
func stripeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 35*time.Second)
}

func (s *StripeRecurring) merchant(ctx context.Context, scope string, enabled bool) (payment.Merchant, error) {
	if s == nil || s.registry == nil || !strings.HasPrefix(scope, "stripe:") {
		return payment.Merchant{}, errStripeObservation
	}
	m, _, err := s.registry.LoadScope(ctx, payment.ProviderStripe, scope)
	if err != nil || m.Scope() != scope || m.Provider != payment.ProviderStripe || (enabled && !m.Enabled) || m.Validate() != nil {
		return payment.Merchant{}, errStripeObservation
	}
	return m, nil
}

// request never follows redirects or exposes provider response bodies in errors.
func (s *StripeRecurring) request(ctx context.Context, m payment.Merchant, method, path string, form url.Values, key string, out any) (int, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.stripe.com"+path, body)
	if err != nil {
		return 0, errStripeObservation
	}
	// A provider idempotency header must not enable net/http transport replay.
	// An accepted POST with a lost acknowledgement is reconciled only by GET.
	if method == http.MethodPost {
		req.GetBody = nil
	}
	req.SetBasicAuth(m.Secrets.SecretKey, "")
	req.Header.Set("Stripe-Version", "2024-06-20")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	client := http.Client{Timeout: 20 * time.Second}
	if s.registry.Client != nil {
		client = *s.registry.Client
	}
	if client.Timeout <= 0 || client.Timeout > 20*time.Second {
		client.Timeout = 20 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return 0, errStripeObservation
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return resp.StatusCode, errStripeObservation
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return resp.StatusCode, errStripeObservation
	}
	return resp.StatusCode, nil
}

func (s *StripeRecurring) verifyAccount(ctx context.Context, m payment.Merchant, charging bool) error {
	var account struct {
		ID           string `json:"id"`
		Charges      bool   `json:"charges_enabled"`
		Capabilities struct {
			Card string `json:"card_payments"`
		} `json:"capabilities"`
	}
	code, err := s.request(ctx, m, http.MethodGet, "/v1/account", nil, "", &account)
	if err != nil || code != 200 || account.ID != m.AccountID || (charging && (!account.Charges || account.Capabilities.Card != "active")) {
		return errStripeObservation
	}
	var balance struct {
		Object string `json:"object"`
		Live   *bool  `json:"livemode"`
	}
	code, err = s.request(ctx, m, http.MethodGet, "/v1/balance", nil, "", &balance)
	if err != nil || code != 200 || balance.Object != "balance" || !stripeLive(m, balance.Live) {
		return errStripeObservation
	}
	return nil
}

// VerifyMerchant checks account identity, card capability and credential mode.
func (s *StripeRecurring) VerifyMerchant(ctx context.Context, requested payment.Merchant) (MerchantEvidence, error) {
	ctx, cancel := stripeContext(ctx)
	defer cancel()
	m, err := s.merchant(ctx, requested.Scope(), false)
	if err != nil || m.ID != requested.ID {
		return MerchantEvidence{}, errStripeObservation
	}
	if err = s.verifyAccount(ctx, m, true); err != nil {
		return MerchantEvidence{}, err
	}
	return MerchantEvidence{Reference: m.Scope() + ":card_off_session", ExpiresAt: time.Now().Add(15 * time.Minute)}, nil
}

type stripeSetup struct {
	ID              string            `json:"id"`
	Object          string            `json:"object"`
	Mode            string            `json:"mode"`
	Status          string            `json:"status"`
	Customer        string            `json:"customer"`
	SetupIntent     string            `json:"setup_intent"`
	Currency        string            `json:"currency"`
	ClientReference string            `json:"client_reference_id"`
	Metadata        map[string]string `json:"metadata"`
	Live            *bool             `json:"livemode"`
	URL             string            `json:"url"`
	Expires         int64             `json:"expires_at"`
}
type stripeInstrument struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	Customer string `json:"customer"`
	Type     string `json:"type"`
	Live     *bool  `json:"livemode"`
	Card     struct {
		Brand string `json:"brand"`
		Last4 string `json:"last4"`
		Month int    `json:"exp_month"`
		Year  int    `json:"exp_year"`
	} `json:"card"`
}
type stripeSealedReference struct {
	Version        int    `json:"version"`
	Account        string `json:"account"`
	Environment    string `json:"environment"`
	Session        string `json:"session"`
	SetupIntent    string `json:"setup_intent"`
	Customer       string `json:"customer"`
	PaymentMethod  string `json:"payment_method"`
	TenantID       int64  `json:"tenant_id"`
	SubscriptionID int64  `json:"subscription_id"`
	ActorID        int64  `json:"actor_id"`
	RequestID      string `json:"request_id"`
	Currency       string `json:"currency"`
	Expires        int64  `json:"expires_at"`
}

func setupMetadata(r HostedAuthorizationRequest) map[string]string {
	return map[string]string{"tenant_id": strconv.FormatInt(r.TenantID, 10), "subscription_id": strconv.FormatInt(r.SubscriptionID, 10), "actor_id": strconv.FormatInt(r.ActorID, 10), "request_id": r.RequestID, "currency": r.Currency}
}
func metadataEqual(actual, expected map[string]string) bool {
	for k, v := range expected {
		if actual[k] != v {
			return false
		}
	}
	return true
}
func putMetadata(form url.Values, prefix string, metadata map[string]string) {
	for k, v := range metadata {
		form.Set(prefix+"["+k+"]", v)
	}
}
func setupKey(scope, nonce, kind string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + nonce + "\x00" + kind))
	return "urmail-" + kind + "-" + hex.EncodeToString(sum[:])
}
func (s *StripeRecurring) setupRequestValid(m payment.Merchant, r HostedAuthorizationRequest) bool {
	if r.TenantID <= 0 || r.SubscriptionID <= 0 || r.ActorID <= 0 || !stripeNonce.MatchString(r.RequestID) || !slices.Contains(m.Currencies, r.Currency) || (r.Currency != "CNY" && r.Currency != "USD") {
		return false
	}
	base, err := url.Parse(s.registry.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil {
		return false
	}
	for _, raw := range []string{r.SuccessURL, r.CancelURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Host != base.Host || u.User != nil || u.Fragment != "" || u.Path == "" || len(raw) > 2048 {
			return false
		}
	}
	return true
}
func setupMatches(m payment.Merchant, v stripeSetup, r HostedAuthorizationRequest) bool {
	return stripeID(v.ID, "cs") && v.Object == "checkout.session" && v.Mode == "setup" && stripeLive(m, v.Live) && v.ClientReference == r.RequestID && metadataEqual(v.Metadata, setupMetadata(r)) && strings.EqualFold(v.Currency, r.Currency)
}
func hostedSession(v stripeSetup) (HostedAuthorizationSession, error) {
	u, err := url.Parse(v.URL)
	if err != nil || u.Scheme != "https" || u.Host != "checkout.stripe.com" || u.User != nil || !strings.HasPrefix(u.Path, "/c/") || len(v.URL) > 8192 || v.Status != "open" || v.Expires <= time.Now().Unix() || v.Expires > time.Now().Add(25*time.Hour).Unix() {
		return HostedAuthorizationSession{}, ErrHostedAuthorizationUncertain
	}
	return HostedAuthorizationSession{Reference: v.ID, URL: v.URL, ExpiresAt: time.Unix(v.Expires, 0)}, nil
}

// StartAuthorization creates a customer and hosted setup with stable provider keys.
func (s *StripeRecurring) StartAuthorization(ctx context.Context, requested payment.Merchant, r HostedAuthorizationRequest) (HostedAuthorizationSession, error) {
	ctx, cancel := stripeContext(ctx)
	defer cancel()
	m, err := s.merchant(ctx, requested.Scope(), true)
	if err != nil || m.ID != requested.ID || !s.setupRequestValid(m, r) {
		return HostedAuthorizationSession{}, errStripeObservation
	}
	if err = s.verifyAccount(ctx, m, true); err != nil {
		return HostedAuthorizationSession{}, err
	}
	form := url.Values{}
	putMetadata(form, "metadata", setupMetadata(r))
	var customer struct {
		ID       string            `json:"id"`
		Object   string            `json:"object"`
		Live     *bool             `json:"livemode"`
		Metadata map[string]string `json:"metadata"`
	}
	code, err := s.request(ctx, m, http.MethodPost, "/v1/customers", form, setupKey(m.Scope(), r.RequestID, "customer"), &customer)
	if err != nil || code != 200 || !stripeID(customer.ID, "cus") || customer.Object != "customer" || !stripeLive(m, customer.Live) || !metadataEqual(customer.Metadata, setupMetadata(r)) {
		return HostedAuthorizationSession{}, ErrHostedAuthorizationUncertain
	}
	form = url.Values{"mode": {"setup"}, "customer": {customer.ID}, "currency": {strings.ToLower(r.Currency)}, "payment_method_types[0]": {"card"}, "success_url": {r.SuccessURL}, "cancel_url": {r.CancelURL}, "client_reference_id": {r.RequestID}}
	putMetadata(form, "metadata", setupMetadata(r))
	putMetadata(form, "setup_intent_data[metadata]", setupMetadata(r))
	var session stripeSetup
	code, err = s.request(ctx, m, http.MethodPost, "/v1/checkout/sessions", form, setupKey(m.Scope(), r.RequestID, "setup"), &session)
	if err != nil || code != 200 || !setupMatches(m, session, r) || session.Customer != customer.ID {
		return HostedAuthorizationSession{}, ErrHostedAuthorizationUncertain
	}
	return hostedSession(session)
}

// LookupAuthorization recovers a setup only from complete bounded GET observations.
func (s *StripeRecurring) LookupAuthorization(ctx context.Context, requested payment.Merchant, r HostedAuthorizationRequest) (HostedAuthorizationSession, error) {
	ctx, cancel := stripeContext(ctx)
	defer cancel()
	m, err := s.merchant(ctx, requested.Scope(), false)
	if err != nil || m.ID != requested.ID || !s.setupRequestValid(m, r) {
		return HostedAuthorizationSession{}, ErrHostedAuthorizationUncertain
	}
	if s.verifyAccount(ctx, m, true) != nil {
		return HostedAuthorizationSession{}, ErrHostedAuthorizationUncertain
	}
	var found []stripeSetup
	after := ""
	seen := map[string]bool{}
	for range stripePageBudget {
		values := url.Values{"limit": {"100"}}
		if after != "" {
			values.Set("starting_after", after)
		}
		var page struct {
			Object string        `json:"object"`
			Data   []stripeSetup `json:"data"`
			More   *bool         `json:"has_more"`
		}
		code, e := s.request(ctx, m, http.MethodGet, "/v1/checkout/sessions?"+values.Encode(), nil, "", &page)
		if e != nil || code != 200 || page.Object != "list" || page.More == nil || len(page.Data) > 100 {
			return HostedAuthorizationSession{}, ErrHostedAuthorizationUncertain
		}
		for _, v := range page.Data {
			if !stripeID(v.ID, "cs") || seen[v.ID] {
				return HostedAuthorizationSession{}, ErrHostedAuthorizationUncertain
			}
			seen[v.ID] = true
			if setupMatches(m, v, r) {
				found = append(found, v)
			}
		}
		if !*page.More {
			if len(found) != 1 {
				return HostedAuthorizationSession{}, ErrHostedAuthorizationUncertain
			}
			return hostedSession(found[0])
		}
		if len(page.Data) == 0 {
			return HostedAuthorizationSession{}, ErrHostedAuthorizationUncertain
		}
		after = page.Data[len(page.Data)-1].ID
	}
	return HostedAuthorizationSession{}, ErrHostedAuthorizationUncertain
}

func (s *StripeRecurring) instrument(ctx context.Context, m payment.Merchant, customer, pm string) (stripeInstrument, time.Time, error) {
	var v stripeInstrument
	if !stripeID(customer, "cus") || !stripeID(pm, "pm") {
		return v, time.Time{}, errStripeObservation
	}
	code, err := s.request(ctx, m, http.MethodGet, "/v1/customers/"+customer+"/payment_methods/"+pm, nil, "", &v)
	if err != nil || code != 200 || v.ID != pm || v.Object != "payment_method" || v.Type != "card" || v.Customer != customer || !stripeLive(m, v.Live) || !stripeLastFour.MatchString(v.Card.Last4) || !stripeBrand.MatchString(v.Card.Brand) || v.Card.Month < 1 || v.Card.Month > 12 || v.Card.Year < time.Now().Year() || v.Card.Year > time.Now().Year()+30 {
		return v, time.Time{}, errStripeObservation
	}
	expiry := time.Date(v.Card.Year, time.Month(v.Card.Month)+1, 1, 0, 0, 0, 0, time.UTC)
	if !expiry.After(time.Now()) {
		return v, time.Time{}, errStripeObservation
	}
	return v, expiry, nil
}

// VerifyAuthorization derives identity and a sealable reference from provider facts.
func (s *StripeRecurring) VerifyAuthorization(ctx context.Context, requested payment.Merchant, reference string) (VerifiedAuthorization, error) {
	ctx, cancel := stripeContext(ctx)
	defer cancel()
	var result VerifiedAuthorization
	m, err := s.merchant(ctx, requested.Scope(), true)
	if err != nil || m.ID != requested.ID || !stripeID(reference, "cs") {
		return result, errStripeObservation
	}
	if s.verifyAccount(ctx, m, true) != nil {
		return result, errStripeObservation
	}
	var cs stripeSetup
	code, err := s.request(ctx, m, http.MethodGet, "/v1/checkout/sessions/"+reference, nil, "", &cs)
	if err != nil || code != 200 || cs.ID != reference || cs.Status != "complete" || !stripeID(cs.Customer, "cus") || !stripeID(cs.SetupIntent, "seti") || cs.Expires <= time.Now().Unix() {
		return result, errStripeObservation
	}
	r := HostedAuthorizationRequest{RequestID: cs.Metadata["request_id"], Currency: cs.Metadata["currency"]}
	r.TenantID, _ = strconv.ParseInt(cs.Metadata["tenant_id"], 10, 64)
	r.SubscriptionID, _ = strconv.ParseInt(cs.Metadata["subscription_id"], 10, 64)
	r.ActorID, _ = strconv.ParseInt(cs.Metadata["actor_id"], 10, 64)
	if r.TenantID <= 0 || r.SubscriptionID <= 0 || r.ActorID <= 0 || !stripeNonce.MatchString(r.RequestID) || !slices.Contains(m.Currencies, r.Currency) || (r.Currency != "USD" && r.Currency != "CNY") || !setupMatches(m, cs, r) {
		return result, errStripeObservation
	}
	var si struct {
		ID       string            `json:"id"`
		Object   string            `json:"object"`
		Status   string            `json:"status"`
		Usage    string            `json:"usage"`
		Customer string            `json:"customer"`
		PM       string            `json:"payment_method"`
		Live     *bool             `json:"livemode"`
		Metadata map[string]string `json:"metadata"`
	}
	code, err = s.request(ctx, m, http.MethodGet, "/v1/setup_intents/"+cs.SetupIntent, nil, "", &si)
	if err != nil || code != 200 || si.ID != cs.SetupIntent || si.Object != "setup_intent" || si.Status != "succeeded" || si.Usage != "off_session" || si.Customer != cs.Customer || !stripeLive(m, si.Live) || !metadataEqual(si.Metadata, setupMetadata(r)) {
		return result, errStripeObservation
	}
	pm, expiry, err := s.instrument(ctx, m, cs.Customer, si.PM)
	if err != nil {
		return result, err
	}
	ref := stripeSealedReference{Version: 1, Account: m.AccountID, Environment: m.Environment, Session: cs.ID, SetupIntent: si.ID, Customer: cs.Customer, PaymentMethod: pm.ID, TenantID: r.TenantID, SubscriptionID: r.SubscriptionID, ActorID: r.ActorID, RequestID: r.RequestID, Currency: r.Currency, Expires: expiry.Unix()}
	raw, err := json.Marshal(ref)
	if err != nil {
		return result, errStripeObservation
	}
	return VerifiedAuthorization{TenantID: r.TenantID, SubscriptionID: r.SubscriptionID, ActorID: r.ActorID, RequestID: r.RequestID, Reference: string(raw), Label: pm.Card.Brand + " •••• " + pm.Card.Last4, Currency: r.Currency, ExpiresAt: expiry}, nil
}

func readStripeReference(raw string, m payment.Merchant, r ChargeRequest) (stripeSealedReference, error) {
	var ref stripeSealedReference
	if len(raw) > 4096 || json.Unmarshal([]byte(raw), &ref) != nil || ref.Version != 1 || ref.Account != m.AccountID || ref.Environment != m.Environment || ref.TenantID != r.TenantID || ref.SubscriptionID != r.SubscriptionID || ref.Currency != r.Currency || ref.ActorID <= 0 || !stripeNonce.MatchString(ref.RequestID) || !stripeID(ref.Customer, "cus") || !stripeID(ref.PaymentMethod, "pm") || !stripeID(ref.Session, "cs") || !stripeID(ref.SetupIntent, "seti") {
		return ref, errStripeObservation
	}
	return ref, nil
}
func chargeMetadata(r ChargeRequest) map[string]string {
	return map[string]string{"idempotency_key": r.IdempotencyKey, "tenant_id": strconv.FormatInt(r.TenantID, 10), "subscription_id": strconv.FormatInt(r.SubscriptionID, 10), "attempt_id": strconv.FormatInt(r.AttemptID, 10), "period_end": r.PeriodEnd.UTC().Format(time.RFC3339Nano)}
}
func validCharge(r ChargeRequest) bool {
	return r.TenantID > 0 && r.SubscriptionID > 0 && r.AttemptID > 0 && r.AmountCents > 0 && r.AmountCents <= 99999999 && (r.Currency == "USD" || r.Currency == "CNY") && !r.PeriodEnd.IsZero() && len(r.IdempotencyKey) > 0 && len(r.IdempotencyKey) <= 250 && strings.TrimSpace(r.IdempotencyKey) == r.IdempotencyKey && !strings.ContainsAny(r.IdempotencyKey, "\r\n\x00")
}

type stripeIntent struct {
	ID            string            `json:"id"`
	Object        string            `json:"object"`
	Status        string            `json:"status"`
	Amount        int64             `json:"amount"`
	Received      *int64            `json:"amount_received"`
	Currency      string            `json:"currency"`
	Customer      string            `json:"customer"`
	PaymentMethod string            `json:"payment_method"`
	Live          *bool             `json:"livemode"`
	Metadata      map[string]string `json:"metadata"`
	LastError     *struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"last_payment_error"`
}

func intentOutcome(m payment.Merchant, r ChargeRequest, ref stripeSealedReference, v stripeIntent) ChargeOutcome {
	unknown := ChargeOutcome{Status: ChargeUncertain}
	if !stripeID(v.ID, "pi") || v.Object != "payment_intent" || !stripeLive(m, v.Live) || v.Amount != r.AmountCents || v.Currency != strings.ToLower(r.Currency) || v.Customer != ref.Customer || v.PaymentMethod != ref.PaymentMethod || !metadataEqual(v.Metadata, chargeMetadata(r)) {
		return unknown
	}
	if v.Status == "succeeded" && v.Received != nil && *v.Received == r.AmountCents {
		return ChargeOutcome{Status: ChargeSucceeded, ProviderTxnID: v.ID, AmountCents: v.Amount, Currency: r.Currency}
	}
	if v.Status == "canceled" && v.Received != nil && *v.Received == 0 {
		return ChargeOutcome{Status: ChargeFailed}
	}
	if v.Status == "requires_payment_method" && v.Received != nil && *v.Received == 0 && v.LastError != nil && v.LastError.Type == "card_error" && slices.Contains([]string{"card_declined", "expired_card", "incorrect_cvc", "incorrect_number", "invalid_number"}, v.LastError.Code) {
		return ChargeOutcome{Status: ChargeFailed}
	}
	return unknown
}

// Charge validates current authority before the only financial POST. Ambiguous
// POST observations remain uncertain; the worker subsequently calls only Query.
func (s *StripeRecurring) Charge(ctx context.Context, r ChargeRequest) (ChargeOutcome, error) {
	ctx, cancel := stripeContext(ctx)
	defer cancel()
	failed := ChargeOutcome{Status: ChargeFailed}
	if !validCharge(r) {
		return failed, nil
	}
	m, err := s.merchant(ctx, r.MerchantScope, true)
	if err != nil || !slices.Contains(m.Currencies, r.Currency) || m.CheckCheckoutAmount(r.Currency, r.AmountCents) != nil {
		return failed, nil
	}
	ref, err := readStripeReference(r.AuthorizationReference, m, r)
	if err != nil || ref.Expires <= time.Now().Unix() {
		return failed, nil
	}
	if s.verifyAccount(ctx, m, true) != nil {
		return failed, nil
	}
	_, _, err = s.instrument(ctx, m, ref.Customer, ref.PaymentMethod)
	if err != nil {
		return failed, nil
	}
	form := url.Values{"amount": {strconv.FormatInt(r.AmountCents, 10)}, "currency": {strings.ToLower(r.Currency)}, "customer": {ref.Customer}, "payment_method": {ref.PaymentMethod}, "confirm": {"true"}, "off_session": {"true"}, "payment_method_types[0]": {"card"}}
	putMetadata(form, "metadata", chargeMetadata(r))
	var response struct {
		stripeIntent
		Error *struct {
			Type   string        `json:"type"`
			Code   string        `json:"code"`
			Intent *stripeIntent `json:"payment_intent"`
		} `json:"error"`
	}
	code, err := s.request(ctx, m, http.MethodPost, "/v1/payment_intents", form, r.IdempotencyKey, &response)
	if err != nil || code >= 500 || code == 0 {
		return ChargeOutcome{Status: ChargeUncertain}, nil
	}
	if code == 200 {
		return intentOutcome(m, r, ref, response.stripeIntent), nil
	}
	if code == 402 && response.Error != nil && response.Error.Intent != nil {
		return intentOutcome(m, r, ref, *response.Error.Intent), nil
	}
	return ChargeOutcome{Status: ChargeUncertain}, nil
}

// Query is entirely read-only, including after account or binding revocation.
// Incomplete listings and absence never establish a safe permission to charge.
func (s *StripeRecurring) Query(ctx context.Context, r ChargeRequest) (ChargeOutcome, error) {
	ctx, cancel := stripeContext(ctx)
	defer cancel()
	unknown := ChargeOutcome{Status: ChargeUncertain}
	if !validCharge(r) {
		return unknown, nil
	}
	m, err := s.merchant(ctx, r.MerchantScope, false)
	if err != nil {
		return unknown, nil
	}
	ref, err := readStripeReference(r.AuthorizationReference, m, r)
	if err != nil {
		return unknown, nil
	}
	if s.verifyAccount(ctx, m, false) != nil {
		return unknown, nil
	}
	var found []string
	after := ""
	seen := map[string]bool{}
	for range stripePageBudget {
		values := url.Values{"customer": {ref.Customer}, "limit": {"100"}}
		if after != "" {
			values.Set("starting_after", after)
		}
		var page struct {
			Object string         `json:"object"`
			Data   []stripeIntent `json:"data"`
			More   *bool          `json:"has_more"`
		}
		code, e := s.request(ctx, m, http.MethodGet, "/v1/payment_intents?"+values.Encode(), nil, "", &page)
		if e != nil || code != 200 || page.Object != "list" || page.More == nil || len(page.Data) > 100 {
			return unknown, nil
		}
		for _, v := range page.Data {
			if !stripeID(v.ID, "pi") || seen[v.ID] {
				return unknown, nil
			}
			seen[v.ID] = true
			if v.Metadata["idempotency_key"] == r.IdempotencyKey {
				found = append(found, v.ID)
			}
		}
		if !*page.More {
			if len(found) != 1 {
				return unknown, nil
			}
			var exact stripeIntent
			code, e = s.request(ctx, m, http.MethodGet, "/v1/payment_intents/"+found[0], nil, "", &exact)
			if e != nil || code != 200 || exact.ID != found[0] {
				return unknown, nil
			}
			return intentOutcome(m, r, ref, exact), nil
		}
		if len(page.Data) == 0 {
			return unknown, nil
		}
		after = page.Data[len(page.Data)-1].ID
	}
	return unknown, nil
}

var _ RecurringAdapter = (*StripeRecurring)(nil)
var _ HostedAuthorizationAdapter = (*StripeRecurring)(nil)
var _ HostedAuthorizationLookup = (*StripeRecurring)(nil)
