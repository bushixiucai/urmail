package billingrenew

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

// ConsentVersion identifies the explicit recurring charge agreement.
const ConsentVersion = "urmail-auto-renew-v1"

// RuleError is a stable product reason without provider credentials or payloads.
type RuleError string

func (e RuleError) Error() string { return string(e) }

// Capability reports the verified recurring support of one merchant account.
type Capability struct {
	MerchantID        int64      `json:"merchant_id"`
	Provider          string     `json:"provider"`
	Name              string     `json:"name"`
	Environment       string     `json:"environment"`
	Status            string     `json:"status"`
	Reason            string     `json:"reason"`
	EvidenceAt        *time.Time `json:"evidence_at"`
	AdapterRegistered bool       `json:"adapter_registered"`
	MerchantApproved  bool       `json:"merchant_approved"`
}

// Binding exposes a verified authorization without its secret reference.
type Binding struct {
	ID         int64      `json:"id"`
	MerchantID int64      `json:"merchant_id"`
	Provider   string     `json:"provider"`
	Label      string     `json:"label"`
	Currency   string     `json:"currency"`
	VerifiedAt time.Time  `json:"verified_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

// Preview freezes the next cycle amount and dates before consent.
type Preview struct {
	Eligible         bool      `json:"eligible"`
	Reason           string    `json:"reason"`
	AmountCents      int64     `json:"amount_cents"`
	AmountCentsExact string    `json:"amount_cents_exact"`
	Currency         string    `json:"currency"`
	BillingCycle     string    `json:"billing_cycle"`
	PeriodStart      time.Time `json:"period_start"`
	PeriodEnd        time.Time `json:"period_end"`
	QuoteDigest      string    `json:"quote_digest"`
}

// Enrollment reports durable consent and revocation state.
type Enrollment struct {
	ID             int64      `json:"id"`
	State          string     `json:"state"`
	Version        int64      `json:"version"`
	BindingID      int64      `json:"binding_id"`
	ConsentVersion string     `json:"consent_version"`
	ConsentedAt    time.Time  `json:"consented_at"`
	RevokedAt      *time.Time `json:"revoked_at"`
}

// Attempt shows financial processing state independently of consent.
type Attempt struct {
	ID               int64     `json:"id"`
	State            string    `json:"state"`
	PeriodEnd        time.Time `json:"period_end"`
	LastError        string    `json:"last_error"`
	InFlight         bool      `json:"in_flight"`
	AmountCentsExact string    `json:"amount_cents_exact"`
	Currency         string    `json:"currency"`
}

// Overview contains the tenant recurring billing capability and consent view.
type Overview struct {
	CanManage      bool         `json:"can_manage"`
	ConsentVersion string       `json:"consent_version"`
	Exempt         bool         `json:"exempt"`
	ExemptReason   string       `json:"exempt_reason"`
	Capabilities   []Capability `json:"capabilities"`
	Bindings       []Binding    `json:"bindings"`
	Preview        *Preview     `json:"preview"`
	Enrollment     *Enrollment  `json:"enrollment"`
	Attempts       []Attempt    `json:"attempts"`
}

// EnrollmentService uses the existing billing tenant lock and immutable paid
// term. A new price, plan, term or merchant configuration requires new consent.
type EnrollmentService struct {
	Dispatcher *Dispatcher
	QuoteKey   []byte
}

func timestamp(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}
func enrollmentView(v store.SubscriptionRenewalEnrollments) *Enrollment {
	return &Enrollment{ID: v.ID, State: v.State, Version: v.Version, BindingID: v.BindingID, ConsentVersion: v.ConsentVersion, ConsentedAt: v.ConsentedAt.Time, RevokedAt: timestamp(v.RevokedAt)}
}

// Overview reads the effective capability and next frozen paid cycle.
func (s EnrollmentService) Overview(ctx context.Context, tenant int64) (Overview, error) {
	if s.Dispatcher == nil || s.Dispatcher.Pool == nil {
		return Overview{}, errors.New("renewal service unavailable")
	}
	return s.overview(ctx, store.New(s.Dispatcher.Pool), tenant)
}
func (s EnrollmentService) overview(ctx context.Context, q *store.Queries, tenant int64) (Overview, error) {
	out := Overview{CanManage: true, ConsentVersion: ConsentVersion, Capabilities: []Capability{}, Bindings: []Binding{}, Attempts: []Attempt{}}
	caps, err := q.PlatformListRecurringCapabilities(ctx)
	if err != nil {
		return out, err
	}
	supported := false
	for _, c := range caps {
		a := s.Dispatcher.Adapters[c.AdapterKey]
		if c.AdapterKey == "" {
			for _, candidate := range s.Dispatcher.Adapters {
				if candidate != nil && candidate.Name() == c.Provider {
					a = candidate
					break
				}
			}
		}
		capability := Capability{MerchantID: c.MerchantID, Provider: c.Provider, Environment: c.Environment, Name: c.Name, Status: "unverified", Reason: "CAPABILITY_UNVERIFIED", EvidenceAt: timestamp(c.EvidenceAt), AdapterRegistered: a != nil && a.Name() == c.Provider, MerchantApproved: c.MerchantApproved}
		if !capability.AdapterRegistered {
			capability.Status = "unsupported"
			capability.Reason = "ADAPTER_UNAVAILABLE"
		} else if s.Dispatcher.adapter(c, false) != nil {
			capability.Status = "supported"
			capability.Reason = ""
			supported = true
		}
		out.Capabilities = append(out.Capabilities, capability)
	}
	attempts, err := q.PlatformListTenantRenewalAttempts(ctx, tenant)
	if err != nil {
		return out, err
	}
	for _, a := range attempts {
		amount := ""
		if a.AmountCents.Valid {
			amount = strconv.FormatInt(a.AmountCents.Int64, 10)
		}
		out.Attempts = append(out.Attempts, Attempt{ID: a.ID, State: a.State, PeriodEnd: a.PeriodEnd.Time, LastError: a.LastError, InFlight: a.State == "charging" || a.State == "uncertain" || a.State == "reconciling", AmountCentsExact: amount, Currency: a.Currency.String})
	}
	sub, err := q.GetActiveSubscriptionByTenant(ctx, tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		out.ExemptReason = "NO_SUBSCRIPTION"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if e, eerr := q.PlatformGetRenewalEnrollment(ctx, store.PlatformGetRenewalEnrollmentParams{TenantID: tenant, SubscriptionID: sub.ID}); eerr == nil {
		out.Enrollment = enrollmentView(e)
	} else if !errors.Is(eerr, pgx.ErrNoRows) {
		return out, eerr
	}
	bindings, err := q.PlatformListRenewalBindings(ctx, store.PlatformListRenewalBindingsParams{TenantID: tenant, SubscriptionID: sub.ID})
	if err != nil {
		return out, err
	}
	for _, b := range bindings {
		for _, c := range out.Capabilities {
			if b.MerchantID == c.MerchantID {
				out.Bindings = append(out.Bindings, Binding{ID: b.ID, MerchantID: b.MerchantID, Provider: c.Provider, Label: b.Label, Currency: b.Currency, VerifiedAt: b.VerifiedAt.Time, ExpiresAt: timestamp(b.ExpiresAt)})
			}
		}
	}
	if out.Enrollment != nil && out.Enrollment.State == "active" {
		valid := false
		for _, b := range bindings {
			if b.ID != out.Enrollment.BindingID || b.ExpiresAt.Valid && !b.ExpiresAt.Time.After(time.Now()) {
				continue
			}
			for _, c := range caps {
				if c.MerchantID == b.MerchantID && s.Dispatcher.adapter(c, false) != nil && b.MerchantConfigAt.Time.Equal(c.MerchantConfigAt.Time) {
					valid = true
				}
			}
		}
		if !valid {
			out.Enrollment.State = "needs_reauthorization"
		}
	}
	if sub.PlanID == "free" || sub.EntitlementSource != "standard" {
		out.Exempt = true
		out.ExemptReason = "FREE_OR_SERVICE"
		if sub.EntitlementSource == billing.EntitlementMigration {
			out.ExemptReason = "MIGRATION_ENTITLEMENT"
		}
		return out, nil
	}
	term, err := q.PlatformGetRenewalFrozenTerm(ctx, store.PlatformGetRenewalFrozenTermParams{TenantID: tenant, PeriodEnd: sub.PeriodEnd})
	if errors.Is(err, pgx.ErrNoRows) {
		out.ExemptReason = "NO_FROZEN_QUOTE"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	p := &Preview{AmountCents: term.BaseAmountCents, AmountCentsExact: strconv.FormatInt(term.BaseAmountCents, 10), Currency: term.Currency, BillingCycle: term.BillingCycle, PeriodStart: sub.PeriodEnd.Time}
	nextEnd, periodErr := renewalEnd(p.PeriodStart, p.BillingCycle, term.CalendarSnapshot)
	if periodErr != nil {
		p.Reason = "NO_FROZEN_QUOTE"
	} else {
		p.PeriodEnd = nextEnd
	}
	if term.BaseAmountCents <= 0 {
		out.Exempt = true
		out.ExemptReason = "FREE_OR_SERVICE"
		return out, nil
	}
	snapshotDigest, digestErr := billing.SnapshotDigest(term.PlanSnapshot)
	if digestErr != nil {
		return out, digestErr
	}
	calendarDigest, calendarErr := billing.SnapshotDigest(term.CalendarSnapshot)
	if calendarErr != nil {
		return out, calendarErr
	}
	raw, _ := json.Marshal(struct {
		Tenant, Subscription, Amount  int64
		Currency, Cycle, Plan, Digest string
		Start, End                    time.Time
		CalendarDigest                string
	}{tenant, sub.ID, p.AmountCents, p.Currency, p.BillingCycle, sub.PlanID, snapshotDigest, p.PeriodStart, p.PeriodEnd, calendarDigest})
	if len(s.QuoteKey) < 32 {
		p.Reason = "NO_FROZEN_QUOTE"
	} else {
		mac := hmac.New(sha256.New, s.QuoteKey)
		_, _ = mac.Write(raw)
		p.QuoteDigest = hex.EncodeToString(mac.Sum(nil))
	}
	if p.Reason == "" {
		switch {
		case sub.CancelAtPeriodEnd:
			p.Reason = "SUBSCRIPTION_CANCELS_AT_END"
		case !supported:
			p.Reason = "NO_SUPPORTED_CHANNEL"
		case term.BillingCycle != sub.BillingCycle:
			p.Reason = "QUOTE_CHANGED"
		default:
			p.Eligible = true
		}
	}
	if _, e := q.GetSubscriptionChange(ctx, tenant); e == nil {
		p.Eligible = false
		p.Reason = "QUOTE_CHANGED"
	} else if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	out.Preview = p
	return out, nil
}

// Enroll validates provider ownership, current quote and explicit consent atomically.
func (s EnrollmentService) Enroll(ctx context.Context, tenant, user, binding int64, version, digest string, confirmed bool) (*Enrollment, error) {
	if !confirmed || version != ConsentVersion {
		return nil, RuleError("EXPLICIT_CONSENT_REQUIRED")
	}
	if s.Dispatcher == nil || s.Dispatcher.Pool == nil {
		return nil, ErrNoChargingChannel
	}
	tx, err := s.Dispatcher.Pool.PlatformTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(ctx, tenant); err != nil {
		return nil, err
	}
	role, roleErr := q.GetUserRoleForTenant(ctx, store.GetUserRoleForTenantParams{UserID: user, TenantID: tenant})
	if roleErr != nil || (role != "owner" && role != "admin" && role != "billing") {
		return nil, RuleError("FORBIDDEN_ROLE")
	}
	out, err := s.overview(ctx, q, tenant)
	if err != nil {
		return nil, err
	}
	if out.Exempt {
		return nil, RuleError(out.ExemptReason)
	}
	if out.Preview == nil {
		return nil, RuleError("NO_FROZEN_QUOTE")
	}
	p := out.Preview
	if !p.Eligible {
		return nil, RuleError(p.Reason)
	}
	if !hmac.Equal([]byte(digest), []byte(p.QuoteDigest)) {
		return nil, RuleError("QUOTE_CHANGED")
	}
	sub, err := q.GetActiveSubscriptionByTenant(ctx, tenant)
	if err != nil {
		return nil, err
	}
	b, err := q.PlatformGetRenewalBinding(ctx, store.PlatformGetRenewalBindingParams{TenantID: tenant, ID: binding})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, RuleError("AUTHORIZATION_REQUIRED")
	}
	if err != nil {
		return nil, err
	}
	if b.SubscriptionID != sub.ID || b.Currency != p.Currency || b.RevokedAt.Valid {
		return nil, RuleError("AUTHORIZATION_REQUIRED")
	}
	if b.ExpiresAt.Valid && !b.ExpiresAt.Time.After(p.PeriodStart) {
		return nil, RuleError("AUTHORIZATION_EXPIRED")
	}
	caps, err := q.PlatformListRecurringCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	valid := false
	for _, c := range caps {
		if c.MerchantID == b.MerchantID && s.Dispatcher.adapter(c, false) != nil && b.MerchantConfigAt.Time.Equal(c.MerchantConfigAt.Time) {
			valid = true
		}
	}
	if !valid {
		return nil, RuleError("NO_SUPPORTED_CHANNEL")
	}
	// Idempotent repeated consent cannot cancel an in-flight attempt.
	if out.Enrollment != nil && out.Enrollment.State == "active" && out.Enrollment.BindingID == binding {
		existing, e := q.PlatformGetRenewalEnrollment(ctx, store.PlatformGetRenewalEnrollmentParams{TenantID: tenant, SubscriptionID: sub.ID})
		if e != nil {
			return nil, e
		}
		if existing.ConsentQuoteDigest == digest && existing.ExpectedPeriodEnd.Time.Equal(sub.PeriodEnd.Time) {
			return out.Enrollment, nil
		}
	}
	for _, a := range out.Attempts {
		if a.InFlight {
			return nil, RuleError("RENEWAL_IN_FLIGHT")
		}
	}
	term, err := q.PlatformGetRenewalFrozenTerm(ctx, store.PlatformGetRenewalFrozenTermParams{TenantID: tenant, PeriodEnd: sub.PeriodEnd})
	if err != nil {
		return nil, err
	}
	if err = q.PlatformRevokeRenewalEnrollment(ctx, store.PlatformRevokeRenewalEnrollmentParams{TenantID: tenant, SubscriptionID: sub.ID}); err != nil {
		return nil, err
	}
	if err = q.PlatformCancelUnstartedRenewals(ctx, store.PlatformCancelUnstartedRenewalsParams{TenantID: tenant, SubscriptionID: sub.ID}); err != nil {
		return nil, err
	}
	snapshotDigest, err := billing.SnapshotDigest(term.PlanSnapshot)
	if err != nil {
		return nil, err
	}
	e, err := q.PlatformInsertRenewalEnrollment(ctx, store.PlatformInsertRenewalEnrollmentParams{TenantID: tenant, SubscriptionID: sub.ID, BindingID: binding, ConsentVersion: version, ConsentedBy: user, ConsentQuoteDigest: digest, AmountCents: p.AmountCents, Currency: p.Currency, BillingCycle: p.BillingCycle, PlanID: sub.PlanID, PlanSnapshot: term.PlanSnapshot, PlanSnapshotDigest: snapshotDigest, ExpectedPeriodEnd: sub.PeriodEnd, RenewalPeriodEnd: pgtype.Timestamptz{Time: p.PeriodEnd, Valid: true}, CalendarSnapshot: term.CalendarSnapshot})
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return enrollmentView(e), nil
}

// Cancel revokes consent and cancels only attempts without a financial intent.
func (s EnrollmentService) Cancel(ctx context.Context, tenant int64, actor ...int64) (*Enrollment, bool, error) {
	if s.Dispatcher == nil || s.Dispatcher.Pool == nil {
		return nil, false, ErrNoChargingChannel
	}
	tx, err := s.Dispatcher.Pool.PlatformTx(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(ctx, tenant); err != nil {
		return nil, false, err
	}
	if len(actor) > 0 {
		role, e := q.GetUserRoleForTenant(ctx, store.GetUserRoleForTenantParams{UserID: actor[0], TenantID: tenant})
		if e != nil || (role != "owner" && role != "admin" && role != "billing") {
			return nil, false, RuleError("FORBIDDEN_ROLE")
		}
	}
	sub, err := q.GetActiveSubscriptionByTenant(ctx, tenant)
	if err != nil {
		return nil, false, err
	}
	if err = q.PlatformRevokeRenewalEnrollment(ctx, store.PlatformRevokeRenewalEnrollmentParams{TenantID: tenant, SubscriptionID: sub.ID}); err != nil {
		return nil, false, err
	}
	if err = q.PlatformCancelUnstartedRenewals(ctx, store.PlatformCancelUnstartedRenewalsParams{TenantID: tenant, SubscriptionID: sub.ID}); err != nil {
		return nil, false, err
	}
	out, err := s.overview(ctx, q, tenant)
	if err != nil {
		return nil, false, err
	}
	flight := false
	for _, a := range out.Attempts {
		flight = flight || a.InFlight
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return out.Enrollment, flight, nil
}

// ApproveMerchant requires a real registered adapter to obtain fresh evidence.
// An operator cannot turn an arbitrary checkout gateway into recurring debit.
func (d *Dispatcher) ApproveMerchant(ctx context.Context, merchantID int64, key string, actor ...int64) error {
	if d == nil || d.Pool == nil || d.Registry == nil {
		return ErrNoChargingChannel
	}
	a := d.Adapters[key]
	if a == nil {
		return ErrNoChargingChannel
	}
	q := store.New(d.Pool)
	snapshot, err := recurringAdminBefore(ctx, q, merchantID, actor)
	if err != nil {
		return err
	}
	m, _, err := d.Registry.Load(ctx, merchantID, true)
	if err != nil {
		return err
	}
	if a.Name() != m.Provider {
		return ErrNoChargingChannel
	}
	evidence, err := a.VerifyMerchant(ctx, m)
	if err != nil {
		return errors.New("merchant recurring verification failed")
	}
	if evidence.Reference == "" || len(evidence.Reference) > 500 || !evidence.ExpiresAt.After(time.Now()) || evidence.ExpiresAt.After(time.Now().AddDate(1, 0, 0)) {
		return RuleError("CAPABILITY_UNVERIFIED")
	}
	tx, err := d.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q = store.New(tx)
	if err = recurringAdminAfter(ctx, q, snapshot, actor); err != nil {
		return err
	}
	if err = q.PlatformApproveRecurringCapability(ctx, store.PlatformApproveRecurringCapabilityParams{MerchantID: merchantID, AdapterKey: key, EvidenceExpiresAt: pgtype.Timestamptz{Time: evidence.ExpiresAt, Valid: true}, EvidenceKind: m.Environment, MerchantConfigAt: snapshot.merchant.UpdatedAt, EvidenceReference: evidence.Reference}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// VerifyBinding must only be called by a privileged operator or a verified
// provider callback. The provider response, not a claimed tenant reference,
// establishes both tenant and subscription ownership.
func (d *Dispatcher) VerifyBinding(ctx context.Context, tenant, subscription, merchantID int64, reference string, actor ...int64) (int64, error) {
	if d == nil || d.Secrets == nil || d.Registry == nil || reference == "" || len(reference) > 4096 {
		return 0, ErrNoChargingChannel
	}
	caps, err := d.capabilities(ctx)
	if err != nil {
		return 0, err
	}
	var adapter RecurringAdapter
	for _, c := range caps {
		if c.MerchantID == merchantID {
			adapter = d.adapter(c, false)
		}
	}
	if adapter == nil {
		return 0, ErrNoChargingChannel
	}
	q := store.New(d.Pool)
	snapshot, err := recurringAdminBefore(ctx, q, merchantID, actor)
	if err != nil {
		return 0, err
	}
	target, err := q.GetActiveSubscriptionByTenant(ctx, tenant)
	if err != nil || target.ID != subscription {
		return 0, RuleError("AUTHORIZATION_REQUIRED")
	}
	merchant := snapshot.merchant

	caps, err = d.capabilities(ctx)
	if err != nil {
		return 0, err
	}
	adapter = nil
	for _, capability := range caps {
		if capability.MerchantID == merchantID {
			adapter = d.adapter(capability, false)
		}
	}
	if adapter == nil {
		return 0, ErrNoChargingChannel
	}
	m, _, err := d.Registry.Load(ctx, merchantID, true)
	if err != nil {
		return 0, err
	}
	verified, err := adapter.VerifyAuthorization(ctx, m, reference)
	if err != nil {
		return 0, errors.New("provider authorization verification failed")
	}
	if verified.TenantID != tenant || verified.SubscriptionID != subscription || verified.Reference == "" || len(verified.Label) > 200 || (verified.Currency != "CNY" && verified.Currency != "USD") || (!verified.ExpiresAt.IsZero() && !verified.ExpiresAt.After(time.Now())) {
		return 0, RuleError("AUTHORIZATION_REQUIRED")
	}
	sealed, err := d.Secrets.EncryptString(verified.Reference)
	if err != nil {
		return 0, err
	}
	if len(sealed) < 7 || sealed[:7] != "enc:v1:" {
		return 0, errors.New("authorization encryption required")
	}
	hash := sha256.Sum256([]byte(m.Scope() + ":" + verified.Reference))
	tx, err := d.Pool.PlatformTx(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q = store.New(tx)
	if err = q.PlatformLockBillingTenant(ctx, tenant); err != nil {
		return 0, err
	}
	if err = recurringAdminAfter(ctx, q, snapshot, actor); err != nil {
		return 0, err
	}
	if _, err = q.PlatformLockRecurringBindingTarget(ctx, store.PlatformLockRecurringBindingTargetParams{TenantID: tenant, ID: subscription}); err != nil {
		return 0, RuleError("AUTHORIZATION_REQUIRED")
	}
	current, err := q.GetActiveSubscriptionByTenant(ctx, tenant)
	if err != nil || current.ID != subscription || !current.PeriodEnd.Time.Equal(target.PeriodEnd.Time) {
		return 0, RuleError("AUTHORIZATION_REQUIRED")
	}
	b, err := q.PlatformInsertRenewalBinding(ctx, store.PlatformInsertRenewalBindingParams{TenantID: tenant, SubscriptionID: subscription, MerchantID: merchantID, AuthorizationCiphertext: sealed, AuthorizationHash: fmt.Sprintf("%x", hash), Label: verified.Label, Currency: verified.Currency, ExpiresAt: pgtype.Timestamptz{Time: verified.ExpiresAt, Valid: !verified.ExpiresAt.IsZero()}, MerchantConfigAt: merchant.UpdatedAt})
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return b.ID, nil
}

func renewalEnd(start time.Time, cycle string, raw []byte) (time.Time, error) {
	if len(raw) == 0 || string(raw) == "{}" {
		return billing.AddCycle(start, cycle)
	}
	var spec billing.CalendarSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return time.Time{}, err
	}
	anchor, err := spec.Anchor()
	if err != nil {
		return time.Time{}, err
	}
	return billing.AddAnchoredCycle(start, cycle, anchor)
}
