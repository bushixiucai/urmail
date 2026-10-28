package billingrenew

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

// HostedSetup exposes only the owner-scoped hosted flow, never provider secrets.
// Completing setup creates a verified binding; enrollment still needs explicit
// consent to the separate, current frozen renewal quote.
type HostedSetup struct {
	ID          int64     `json:"id"`
	MerchantID  int64     `json:"merchant_id"`
	State       string    `json:"state"`
	Version     int64     `json:"version"`
	URL         string    `json:"url,omitempty"`
	Currency    string    `json:"currency"`
	QuoteDigest string    `json:"quote_digest"`
	ExpiresAt   time.Time `json:"expires_at"`
	BindingID   *int64    `json:"binding_id"`
	LastError   string    `json:"last_error"`
}

func hostedView(row store.RecurringSetupIntents) HostedSetup {
	out := HostedSetup{ID: row.ID, MerchantID: row.MerchantID, State: row.State, Version: row.Version, Currency: row.Currency, QuoteDigest: row.QuoteDigest, ExpiresAt: row.ExpiresAt.Time, LastError: row.LastError}
	if row.State == "ready" && row.ExpiresAt.Time.After(time.Now()) {
		out.URL = row.HostedUrl
	}
	if !row.ExpiresAt.Time.After(time.Now()) && (row.State == "ready" || row.State == "creating" || row.State == "uncertain") {
		out.State = "expired"
	}
	if row.BindingID.Valid {
		id := row.BindingID.Int64
		out.BindingID = &id
	}
	return out
}
func setupUpdate(ctx context.Context, q *store.Queries, row store.RecurringSetupIntents) (store.RecurringSetupIntents, error) {
	return q.PlatformUpdateRecurringSetup(ctx, store.PlatformUpdateRecurringSetupParams{TenantID: row.TenantID, ActorID: row.ActorID, ID: row.ID, ExpectedVersion: row.Version, State: row.State, ProviderReference: row.ProviderReference, HostedUrl: row.HostedUrl, ExpiresAt: row.ExpiresAt, BindingID: row.BindingID, LastError: row.LastError})
}
func setupAudit(ctx context.Context, q *store.Queries, row store.RecurringSetupIntents, action string) error {
	return q.PlatformAuditRecurringSetup(ctx, store.PlatformAuditRecurringSetupParams{TenantID: row.TenantID, ActorID: row.ActorID, ID: row.ID, Action: "billing.recurring_setup." + action})
}
func setupActor(ctx context.Context, q *store.Queries, tenant, actor int64) error {
	if tenant <= 0 || actor <= 0 {
		return RuleError("FORBIDDEN_ROLE")
	}
	_, e := q.PlatformLockRecurringSetupActor(ctx, store.PlatformLockRecurringSetupActorParams{TenantID: tenant, UserID: actor})
	if e != nil {
		return RuleError("FORBIDDEN_ROLE")
	}
	return nil
}
func (s EnrollmentService) setupAvailable() error {
	if s.Dispatcher == nil || s.Dispatcher.Pool == nil || s.Dispatcher.Registry == nil || s.Dispatcher.Secrets == nil {
		return ErrNoChargingChannel
	}
	return nil
}
func (s EnrollmentService) setupChannel(ctx context.Context, q *store.Queries, id int64) (RecurringAdapter, store.PaymentMerchants, error) {
	merchant, e := q.PlatformLockRecurringSetupMerchant(ctx, id)
	if e != nil {
		return nil, merchant, e
	}
	if _, e = q.PlatformLockRecurringSetupCapability(ctx, id); e != nil {
		return nil, merchant, ErrNoChargingChannel
	}
	caps, e := q.PlatformListRecurringCapabilities(ctx)
	if e != nil {
		return nil, merchant, e
	}
	for _, capability := range caps {
		if capability.MerchantID == id {
			adapter := s.Dispatcher.adapter(capability, false)
			if adapter != nil {
				return adapter, merchant, nil
			}
		}
	}
	return nil, merchant, ErrNoChargingChannel
}
func (s EnrollmentService) setupCurrent(ctx context.Context, q *store.Queries, row store.RecurringSetupIntents) (RecurringAdapter, error) {
	if e := setupActor(ctx, q, row.TenantID, row.ActorID); e != nil {
		return nil, e
	}
	a, m, e := s.setupChannel(ctx, q, row.MerchantID)
	if e != nil {
		return nil, e
	}
	if !m.UpdatedAt.Time.Equal(row.MerchantConfigAt.Time) || !slices.Contains(m.Currencies, row.Currency) {
		return nil, RuleError("HOSTED_SETUP_CHANGED")
	}
	sub, e := q.GetActiveSubscriptionByTenant(ctx, row.TenantID)
	if e != nil {
		return nil, e
	}
	if sub.ID != row.SubscriptionID || !sub.PeriodEnd.Time.Equal(row.ExpectedPeriodEnd.Time) {
		return nil, RuleError("QUOTE_CHANGED")
	}
	out, e := s.overview(ctx, q, row.TenantID)
	if e != nil {
		return nil, e
	}
	if out.Preview == nil || !out.Preview.Eligible || !hmac.Equal([]byte(out.Preview.QuoteDigest), []byte(row.QuoteDigest)) || out.Preview.Currency != row.Currency {
		return nil, RuleError("QUOTE_CHANGED")
	}
	return a, nil
}
func (s EnrollmentService) hostedRequest(row store.RecurringSetupIntents) (HostedAuthorizationRequest, error) {
	origin, e := url.Parse(s.Dispatcher.Registry.BaseURL)
	if e != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" {
		return HostedAuthorizationRequest{}, RuleError("HOSTED_SETUP_UNAVAILABLE")
	}
	origin.Path = strings.TrimRight(origin.Path, "/") + "/billing"
	values := url.Values{"recurring_setup": []string{fmt.Sprint(row.ID)}}
	origin.RawQuery = values.Encode()
	success := origin.String()
	values.Set("setup_canceled", "1")
	origin.RawQuery = values.Encode()
	return HostedAuthorizationRequest{TenantID: row.TenantID, SubscriptionID: row.SubscriptionID, ActorID: row.ActorID, RequestID: row.RequestID, Currency: row.Currency, SuccessURL: success, CancelURL: origin.String()}, nil
}

// StartHostedSetup commits a single provider-creation intent before POST. The
// same request key always returns that intent; uncertainty never creates again.
func (s EnrollmentService) StartHostedSetup(ctx context.Context, tenant, actor, merchant int64, key, digest string, confirmed bool) (HostedSetup, error) {
	if e := s.setupAvailable(); e != nil {
		return HostedSetup{}, e
	}
	if !confirmed || len(key) < 8 || len(key) > 200 || len(digest) != 64 {
		return HostedSetup{}, RuleError("HOSTED_SETUP_CONFIRM_REQUIRED")
	}
	tx, e := s.Dispatcher.Pool.PlatformTx(ctx)
	if e != nil {
		return HostedSetup{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if e = q.PlatformLockBillingTenant(ctx, tenant); e != nil {
		return HostedSetup{}, e
	}
	if e = setupActor(ctx, q, tenant, actor); e != nil {
		return HostedSetup{}, e
	}
	prior, e := q.PlatformGetRecurringSetupByKey(ctx, store.PlatformGetRecurringSetupByKeyParams{TenantID: tenant, ActorID: actor, RequestKey: key})
	if e == nil {
		if prior.MerchantID != merchant || prior.QuoteDigest != digest {
			return HostedSetup{}, RuleError("HOSTED_SETUP_SOURCE_CONFLICT")
		}
		return hostedView(prior), nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return HostedSetup{}, e
	}
	a, m, e := s.setupChannel(ctx, q, merchant)
	if e != nil {
		return HostedSetup{}, e
	}
	starter, ok := a.(HostedAuthorizationAdapter)
	if !ok {
		return HostedSetup{}, RuleError("HOSTED_SETUP_UNAVAILABLE")
	}
	out, e := s.overview(ctx, q, tenant)
	if e != nil {
		return HostedSetup{}, e
	}
	if out.Exempt || out.Preview == nil || !out.Preview.Eligible || !hmac.Equal([]byte(out.Preview.QuoteDigest), []byte(digest)) {
		return HostedSetup{}, RuleError("QUOTE_CHANGED")
	}
	if !slices.Contains(m.Currencies, out.Preview.Currency) {
		return HostedSetup{}, RuleError("NO_SUPPORTED_CHANNEL")
	}
	sub, e := q.GetActiveSubscriptionByTenant(ctx, tenant)
	if e != nil {
		return HostedSetup{}, e
	}
	count, e := q.PlatformCountOpenRecurringSetups(ctx, tenant)
	if e != nil {
		return HostedSetup{}, e
	}
	if count >= 5 {
		return HostedSetup{}, RuleError("HOSTED_SETUP_LIMIT")
	}
	row, e := q.PlatformCreateRecurringSetup(ctx, store.PlatformCreateRecurringSetupParams{TenantID: tenant, SubscriptionID: sub.ID, ActorID: actor, MerchantID: merchant, RequestKey: key, RequestID: uuid.NewString(), QuoteDigest: digest, Currency: out.Preview.Currency, ExpectedPeriodEnd: sub.PeriodEnd, MerchantConfigAt: m.UpdatedAt})
	if e != nil {
		return HostedSetup{}, e
	}
	request, e := s.hostedRequest(row)
	if e != nil {
		return HostedSetup{}, e
	}
	if e = setupAudit(ctx, q, row, "intent"); e != nil {
		return HostedSetup{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return HostedSetup{}, e
	}
	provider, _, e := s.Dispatcher.Registry.Load(ctx, merchant, true)
	var session HostedAuthorizationSession
	if e == nil {
		callctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		session, e = starter.StartAuthorization(callctx, provider, request)
		cancel()
	}
	// A disconnected caller cannot erase the already-started provider operation.
	finishctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.recordHostedSession(finishctx, row, session, e)
}
func validHostedSession(session HostedAuthorizationSession) bool {
	u, e := url.Parse(session.URL)
	return e == nil && u.Scheme == "https" && u.Host == "checkout.stripe.com" && u.User == nil && len(session.URL) <= 4096 && strings.HasPrefix(session.Reference, "cs_") && len(session.Reference) <= 200 && session.ExpiresAt.After(time.Now()) && session.ExpiresAt.Before(time.Now().Add(25*time.Hour))
}
func (s EnrollmentService) recordHostedSession(ctx context.Context, original store.RecurringSetupIntents, session HostedAuthorizationSession, providerErr error) (HostedSetup, error) {
	tx, e := s.Dispatcher.Pool.PlatformTx(ctx)
	if e != nil {
		return HostedSetup{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if e = q.PlatformLockBillingTenant(ctx, original.TenantID); e != nil {
		return HostedSetup{}, e
	}
	row, e := q.PlatformLockRecurringSetup(ctx, store.PlatformLockRecurringSetupParams{TenantID: original.TenantID, ActorID: original.ActorID, ID: original.ID})
	if e != nil {
		return HostedSetup{}, e
	}
	// Cancellation, restore and another completed reconciliation fence old I/O.
	// Retain an actually created provider identity after cancellation/restore,
	// without restoring its URL, eligibility or consumed proof.
	if (row.State == "canceled" || row.State == "held") && row.ProviderReference == "" && providerErr == nil && validHostedSession(session) {
		row.ProviderReference = session.Reference
		row, e = setupUpdate(ctx, q, row)
		if e != nil {
			return HostedSetup{}, e
		}
		if e = setupAudit(ctx, q, row, "late_provider_fact"); e != nil {
			return HostedSetup{}, e
		}
		if e = tx.Commit(ctx); e != nil {
			return HostedSetup{}, e
		}
		return hostedView(row), nil
	}

	if row.Version != original.Version || row.State != "creating" && row.State != "uncertain" {
		return hostedView(row), nil
	}
	if _, e = s.setupCurrent(ctx, q, row); e != nil {
		row.State = "held"
		row.LastError = "HOSTED_SETUP_CHANGED"
		if providerErr == nil && validHostedSession(session) {
			row.ProviderReference = session.Reference
			row.HostedUrl = ""
		}
	} else if providerErr != nil || !validHostedSession(session) {
		row.State = "uncertain"
		row.LastError = "HOSTED_SETUP_UNKNOWN"
	} else {
		row.State = "ready"
		row.ProviderReference = session.Reference
		row.HostedUrl = session.URL
		row.ExpiresAt = pgtype.Timestamptz{Time: session.ExpiresAt, Valid: true}
		row.LastError = ""
	}
	row, e = setupUpdate(ctx, q, row)
	if e != nil {
		return HostedSetup{}, e
	}
	if e = setupAudit(ctx, q, row, "provider_result"); e != nil {
		return HostedSetup{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return HostedSetup{}, e
	}
	return hostedView(row), nil
}

// ListHostedSetups permits only the original actor with a currently live role.
func (s EnrollmentService) ListHostedSetups(ctx context.Context, tenant, actor int64, key string) ([]HostedSetup, error) {
	if e := s.setupAvailable(); e != nil {
		return nil, e
	}
	tx, e := s.Dispatcher.Pool.PlatformTx(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if e = setupActor(ctx, q, tenant, actor); e != nil {
		return nil, e
	}
	rows := []store.RecurringSetupIntents{}
	if key != "" {
		row, err := q.PlatformGetRecurringSetupByKey(ctx, store.PlatformGetRecurringSetupByKeyParams{TenantID: tenant, ActorID: actor, RequestKey: key})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			rows = append(rows, row)
		}
	} else {
		rows, e = q.PlatformListRecurringSetups(ctx, store.PlatformListRecurringSetupsParams{TenantID: tenant, ActorID: actor})
		if e != nil {
			return nil, e
		}
	}
	result := make([]HostedSetup, 0, len(rows))
	for _, row := range rows {
		result = append(result, hostedView(row))
	}
	return result, nil
}

// GetHostedSetup reads one original actor-scoped receipt with a fresh role check.
func (s EnrollmentService) GetHostedSetup(ctx context.Context, tenant, actor, id int64) (HostedSetup, error) {
	if e := s.setupAvailable(); e != nil {
		return HostedSetup{}, e
	}
	tx, e := s.Dispatcher.Pool.PlatformTx(ctx)
	if e != nil {
		return HostedSetup{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if e = setupActor(ctx, q, tenant, actor); e != nil {
		return HostedSetup{}, e
	}
	row, e := q.PlatformGetRecurringSetup(ctx, store.PlatformGetRecurringSetupParams{TenantID: tenant, ActorID: actor, ID: id})
	return hostedView(row), e
}

// ReconcileHostedSetup performs only provider GET/list operations. Missing or
// truncated results keep uncertainty; it never repeats StartAuthorization.
func (s EnrollmentService) ReconcileHostedSetup(ctx context.Context, tenant, actor, id, version int64) (HostedSetup, error) {
	if e := s.setupAvailable(); e != nil {
		return HostedSetup{}, e
	}
	tx, e := s.Dispatcher.Pool.PlatformTx(ctx)
	if e != nil {
		return HostedSetup{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if e = setupActor(ctx, q, tenant, actor); e != nil {
		return HostedSetup{}, e
	}
	row, e := q.PlatformGetRecurringSetup(ctx, store.PlatformGetRecurringSetupParams{TenantID: tenant, ActorID: actor, ID: id})
	if e != nil {
		return HostedSetup{}, e
	}
	if row.Version != version || row.State != "creating" && row.State != "uncertain" || !row.ExpiresAt.Time.After(time.Now()) {
		return HostedSetup{}, RuleError("HOSTED_SETUP_CHANGED")
	}
	a, e := s.setupCurrent(ctx, q, row)
	if e != nil {
		return HostedSetup{}, e
	}
	lookup, ok := a.(HostedAuthorizationLookup)
	if !ok {
		return HostedSetup{}, RuleError("HOSTED_SETUP_UNAVAILABLE")
	}
	request, e := s.hostedRequest(row)
	if e != nil {
		return HostedSetup{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return HostedSetup{}, e
	}
	merchant, _, e := s.Dispatcher.Registry.Load(ctx, row.MerchantID, true)
	if e != nil {
		return HostedSetup{}, e
	}
	callctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	session, e := lookup.LookupAuthorization(callctx, merchant, request)
	cancel()
	return s.recordHostedSession(ctx, row, session, e)
}

// CompleteHostedSetup uses only the persisted provider session. Caller-supplied
// provider references, payment methods and ownership metadata are never accepted.
func (s EnrollmentService) CompleteHostedSetup(ctx context.Context, tenant, actor, id, version int64) (HostedSetup, error) {
	if e := s.setupAvailable(); e != nil {
		return HostedSetup{}, e
	}
	tx, e := s.Dispatcher.Pool.PlatformTx(ctx)
	if e != nil {
		return HostedSetup{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if e = setupActor(ctx, q, tenant, actor); e != nil {
		return HostedSetup{}, e
	}
	row, e := q.PlatformGetRecurringSetup(ctx, store.PlatformGetRecurringSetupParams{TenantID: tenant, ActorID: actor, ID: id})
	if e != nil {
		return HostedSetup{}, e
	}
	if row.State == "verified" {
		return hostedView(row), nil
	}
	if row.Version != version || row.State != "ready" || row.ProviderReference == "" || !row.ExpiresAt.Time.After(time.Now()) {
		return HostedSetup{}, RuleError("HOSTED_SETUP_CHANGED")
	}
	a, e := s.setupCurrent(ctx, q, row)
	if e != nil {
		return HostedSetup{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return HostedSetup{}, e
	}
	merchant, _, e := s.Dispatcher.Registry.Load(ctx, row.MerchantID, true)
	if e != nil {
		return HostedSetup{}, e
	}
	callctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	verified, e := a.VerifyAuthorization(callctx, merchant, row.ProviderReference)
	cancel()
	if e != nil {
		return HostedSetup{}, RuleError("AUTHORIZATION_REQUIRED")
	}
	if verified.TenantID != tenant || verified.SubscriptionID != row.SubscriptionID || verified.ActorID != actor || verified.RequestID != row.RequestID || verified.Currency != row.Currency || verified.Reference == "" || len(verified.Reference) > 4096 || len(verified.Label) > 200 || !verified.ExpiresAt.IsZero() && !verified.ExpiresAt.After(time.Now()) {
		return HostedSetup{}, RuleError("AUTHORIZATION_REQUIRED")
	}
	sealed, e := s.Dispatcher.Secrets.EncryptString(verified.Reference)
	if e != nil || !strings.HasPrefix(sealed, "enc:v1:") {
		return HostedSetup{}, RuleError("AUTHORIZATION_REQUIRED")
	}
	tx, e = s.Dispatcher.Pool.PlatformTx(ctx)
	if e != nil {
		return HostedSetup{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q = store.New(tx)
	if e = q.PlatformLockBillingTenant(ctx, tenant); e != nil {
		return HostedSetup{}, e
	}
	current, e := q.PlatformLockRecurringSetup(ctx, store.PlatformLockRecurringSetupParams{TenantID: tenant, ActorID: actor, ID: id})
	if e != nil {
		return HostedSetup{}, e
	}
	if current.State == "verified" {
		return hostedView(current), nil
	}
	if current.Version != row.Version || current.State != "ready" || !current.ExpiresAt.Time.After(time.Now()) {
		return HostedSetup{}, RuleError("HOSTED_SETUP_CHANGED")
	}
	if _, e = s.setupCurrent(ctx, q, current); e != nil {
		return HostedSetup{}, e
	}
	hash := sha256.Sum256([]byte(merchant.Scope() + ":" + verified.Reference))
	binding, e := q.PlatformInsertRenewalBinding(ctx, store.PlatformInsertRenewalBindingParams{TenantID: tenant, SubscriptionID: row.SubscriptionID, MerchantID: row.MerchantID, AuthorizationCiphertext: sealed, AuthorizationHash: fmt.Sprintf("%x", hash), Label: verified.Label, Currency: verified.Currency, ExpiresAt: pgtype.Timestamptz{Time: verified.ExpiresAt, Valid: !verified.ExpiresAt.IsZero()}, MerchantConfigAt: row.MerchantConfigAt})
	if e != nil {
		return HostedSetup{}, e
	}
	current.State = "verified"
	current.BindingID = pgtype.Int8{Int64: binding.ID, Valid: true}
	current.HostedUrl = ""
	current.LastError = ""
	current, e = setupUpdate(ctx, q, current)
	if e != nil {
		return HostedSetup{}, e
	}
	if e = setupAudit(ctx, q, current, "verified"); e != nil {
		return HostedSetup{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return HostedSetup{}, e
	}
	return hostedView(current), nil
}

// CancelHostedSetup invalidates local proof without charging or enrolling.
func (s EnrollmentService) CancelHostedSetup(ctx context.Context, tenant, actor, id, version int64) (HostedSetup, error) {
	if e := s.setupAvailable(); e != nil {
		return HostedSetup{}, e
	}
	tx, e := s.Dispatcher.Pool.PlatformTx(ctx)
	if e != nil {
		return HostedSetup{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if e = q.PlatformLockBillingTenant(ctx, tenant); e != nil {
		return HostedSetup{}, e
	}
	if e = setupActor(ctx, q, tenant, actor); e != nil {
		return HostedSetup{}, e
	}
	row, e := q.PlatformLockRecurringSetup(ctx, store.PlatformLockRecurringSetupParams{TenantID: tenant, ActorID: actor, ID: id})
	if e != nil {
		return HostedSetup{}, e
	}
	if row.State == "canceled" {
		return hostedView(row), nil
	}
	if row.Version != version || row.State == "verified" {
		return HostedSetup{}, RuleError("HOSTED_SETUP_CHANGED")
	}
	row.State = "canceled"
	row.HostedUrl = ""
	row.LastError = ""
	row, e = setupUpdate(ctx, q, row)
	if e != nil {
		return HostedSetup{}, e
	}
	if e = setupAudit(ctx, q, row, "canceled"); e != nil {
		return HostedSetup{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return HostedSetup{}, e
	}
	return hostedView(row), nil
}
