package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/billingrenew"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

type renewalFixture struct {
	tenantID       int64
	subscriptionID int64
	attemptID      int64
	periodEnd      time.Time
}

// newRenewalFixture creates a lapsed monthly subscription with one pending
// renewal attempt. tenantStatus sets the independent tenant suspension state
// so settlement tests can prove only billing state is restored.
func newRenewalFixture(ctx context.Context, t *testing.T, platform *db.Pool, tenantStatus string) renewalFixture {
	t.Helper()
	fx := renewalFixture{periodEnd: time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)}
	var brandID int64
	if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES ('renew-fixture','Renew') RETURNING id").Scan(&brandID); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx,
		"INSERT INTO tenants(brand_id,name,slug,status) VALUES ($1,'Renew','renew-fixture',$2) RETURNING id",
		brandID, tenantStatus).Scan(&fx.tenantID); err != nil {
		t.Fatal(err)
	}
	var code string
	if err := platform.QueryRow(ctx,
		"INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit)"+
			" VALUES ('renew-pro','{}',1000,'CNY',1073741824,5,2,100)"+
			" ON CONFLICT (code) DO UPDATE SET price_cents=EXCLUDED.price_cents RETURNING code").Scan(&code); err != nil {
		t.Fatal(err)
	}
	// Migration 0015 already created the tenant's one allowed subscription
	// row ('free', active); repurpose it instead of inserting a second row,
	// which would violate subscriptions_one_active_per_tenant.
	if err := platform.QueryRow(ctx,
		"UPDATE subscriptions SET plan_id='renew-pro', status='past_due', billing_cycle='monthly',"+
			" period_start=$2, period_end=$3, cancel_at_period_end=false WHERE tenant_id=$1 RETURNING id",
		fx.tenantID, fx.periodEnd.Add(-30*24*time.Hour), fx.periodEnd).Scan(&fx.subscriptionID); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx,
		"INSERT INTO subscription_renewal_attempts(tenant_id,subscription_id,period_end,state)"+
			" VALUES ($1,$2,$3,'pending') RETURNING id",
		fx.tenantID, fx.subscriptionID, fx.periodEnd).Scan(&fx.attemptID); err != nil {
		t.Fatal(err)
	}
	return fx
}

func countRows(ctx context.Context, t *testing.T, platform *db.Pool, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := platform.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func periodEndOf(ctx context.Context, t *testing.T, platform *db.Pool, subscriptionID int64) time.Time {
	t.Helper()
	var end time.Time
	if err := platform.QueryRow(ctx, "SELECT period_end FROM subscriptions WHERE id=$1", subscriptionID).Scan(&end); err != nil {
		t.Fatal(err)
	}
	return end
}

func settleParams(fx renewalFixture, txn string) billingrenew.RenewalSettlementParams {
	return billingrenew.RenewalSettlementParams{
		TenantID: fx.tenantID, AttemptID: fx.attemptID,
		Provider: "stripe", ProviderTxnID: txn,
		AmountCents: 1000, Currency: "CNY",
	}
}

func TestSettleRenewalAttemptDoubleSettlementIsIdempotent(t *testing.T) {
	ctx := context.Background()
	platform, _ := testdb.OpenMigrated(t)
	fx := newRenewalFixture(ctx, t, platform, "active")

	first, err := billingrenew.PlatformSettleRenewalAttempt(ctx, platform, settleParams(fx, "txn_double"))
	if err != nil || first.AlreadySettled {
		t.Fatalf("first settlement: result=%+v err=%v", first, err)
	}
	second, err := billingrenew.PlatformSettleRenewalAttempt(ctx, platform, settleParams(fx, "txn_double"))
	if err != nil || !second.AlreadySettled {
		t.Fatalf("second settlement: result=%+v err=%v", second, err)
	}
	if n := countRows(ctx, t, platform, "SELECT count(*) FROM payments WHERE provider_txn_id='txn_double'"); n != 1 {
		t.Fatalf("payments for txn_double = %d, want 1", n)
	}
	if end := periodEndOf(ctx, t, platform, fx.subscriptionID); !end.Equal(first.PeriodEnd) {
		t.Fatalf("period moved again: %v != %v", end, first.PeriodEnd)
	}
	var state string
	if err := platform.QueryRow(ctx, "SELECT state FROM subscription_renewal_attempts WHERE id=$1", fx.attemptID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" {
		t.Fatalf("attempt state = %q", state)
	}
}

func TestSettleRenewalAttemptConcurrentProducesOnePayment(t *testing.T) {
	ctx := context.Background()
	platform, _ := testdb.OpenMigrated(t)
	fx := newRenewalFixture(ctx, t, platform, "active")

	var wg sync.WaitGroup
	results := make([]billingrenew.RenewalSettlement, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			results[slot], errs[slot] = billingrenew.PlatformSettleRenewalAttempt(ctx, platform, settleParams(fx, "txn_concurrent"))
		}(i)
	}
	wg.Wait()
	for slot, err := range errs {
		if err != nil {
			t.Fatalf("concurrent settlement %d: %v", slot, err)
		}
	}
	if results[0].AlreadySettled == results[1].AlreadySettled {
		t.Fatalf("exactly one caller must apply the settlement: %+v", results)
	}
	if n := countRows(ctx, t, platform, "SELECT count(*) FROM payments WHERE provider_txn_id='txn_concurrent'"); n != 1 {
		t.Fatalf("payments = %d, want exactly one", n)
	}
	wantEnd := results[0].PeriodEnd
	if results[1].PeriodEnd.After(wantEnd) {
		wantEnd = results[1].PeriodEnd
	}
	if end := periodEndOf(ctx, t, platform, fx.subscriptionID); !end.Equal(wantEnd) || !wantEnd.After(fx.periodEnd) {
		t.Fatalf("period not extended exactly once: %v", end)
	}
}

func TestRouteConfirmedWebhookAfterSettlementIsNoOp(t *testing.T) {
	ctx := context.Background()
	platform, _ := testdb.OpenMigrated(t)
	fx := newRenewalFixture(ctx, t, platform, "active")

	result, err := billingrenew.PlatformSettleRenewalAttempt(ctx, platform, settleParams(fx, "txn_late"))
	if err != nil {
		t.Fatal(err)
	}
	routed, err := billingrenew.RouteConfirmedWebhook(ctx, platform, billingrenew.ConfirmedRenewalEvent{
		Provider:      "stripe",
		AccountScope:  billingrenew.RenewalAccountScope,
		ProviderTxnID: "txn_late",
		AmountCents:   1000,
		Currency:      "CNY",
		Raw:           []byte("{\"idempotency_key\":\"late\"}"),
	})
	if err != nil || !routed {
		t.Fatalf("out-of-order webhook: routed=%v err=%v", routed, err)
	}
	if n := countRows(ctx, t, platform, "SELECT count(*) FROM payments WHERE provider_txn_id='txn_late'"); n != 1 {
		t.Fatalf("payments = %d, want exactly one", n)
	}
	if end := periodEndOf(ctx, t, platform, fx.subscriptionID); !end.Equal(result.PeriodEnd) {
		t.Fatalf("period extended again by a late webhook: %v != %v", end, result.PeriodEnd)
	}
	unrelated, err := billingrenew.RouteConfirmedWebhook(ctx, platform, billingrenew.ConfirmedRenewalEvent{
		Provider: "stripe", ProviderTxnID: "txn_order_only", AmountCents: 1000, Currency: "CNY",
	})
	if err != nil || unrelated {
		t.Fatalf("order-only webhook must not route: routed=%v err=%v", unrelated, err)
	}
}

func TestRouteConfirmedWebhookSettlesByDedupeKeyExactlyOnce(t *testing.T) {
	ctx := context.Background()
	platform, _ := testdb.OpenMigrated(t)
	fx := newRenewalFixture(ctx, t, platform, "active")
	key := billingrenew.RenewalDedupeKey(fx.subscriptionID, fx.periodEnd, fx.attemptID)
	if err := store.New(platform).PlatformSetRenewalDedupeKey(ctx,
		store.PlatformSetRenewalDedupeKeyParams{ID: fx.attemptID, DedupeKey: key, TenantID: fx.tenantID}); err != nil {
		t.Fatal(err)
	}
	event := billingrenew.ConfirmedRenewalEvent{
		Provider: "stripe", ProviderTxnID: "txn_keyed",
		AmountCents: 1000, Currency: "CNY",
		Raw: []byte("{\"metadata\":{\"idempotency_key\":\"" + key + "\"}}"),
	}
	for i := 0; i < 3; i++ {
		routed, err := billingrenew.RouteConfirmedWebhook(ctx, platform, event)
		if err != nil || !routed {
			t.Fatalf("delivery %d: routed=%v err=%v", i, routed, err)
		}
	}
	if n := countRows(ctx, t, platform, "SELECT count(*) FROM payments WHERE provider_txn_id='txn_keyed'"); n != 1 {
		t.Fatalf("duplicate deliveries produced %d payments, want 1", n)
	}
	if end := periodEndOf(ctx, t, platform, fx.subscriptionID); end.Before(fx.periodEnd.Add(time.Hour)) {
		t.Fatalf("period not extended: %v", end)
	}
}

func TestSettlementKeepsSuspensionUntouched(t *testing.T) {
	ctx := context.Background()
	platform, _ := testdb.OpenMigrated(t)
	fx := newRenewalFixture(ctx, t, platform, "suspended")

	if _, err := billingrenew.PlatformSettleRenewalAttempt(ctx, platform, settleParams(fx, "txn_suspended")); err != nil {
		t.Fatal(err)
	}
	var tenantStatus, subStatus string
	if err := platform.QueryRow(ctx, "SELECT status FROM tenants WHERE id=$1", fx.tenantID).Scan(&tenantStatus); err != nil {
		t.Fatal(err)
	}
	if tenantStatus != "suspended" {
		t.Fatalf("tenant suspension was cleared by settlement: %q", tenantStatus)
	}
	if err := platform.QueryRow(ctx, "SELECT status FROM subscriptions WHERE id=$1", fx.subscriptionID).Scan(&subStatus); err != nil {
		t.Fatal(err)
	}
	if subStatus != "active" {
		t.Fatalf("billing status not restored: %q", subStatus)
	}
}

func TestFailedRenewalAttemptsBackOffAndCapAtFive(t *testing.T) {
	ctx := context.Background()
	platform, _ := testdb.OpenMigrated(t)
	fx := newRenewalFixture(ctx, t, platform, "active")
	q := store.New(platform)

	var lastNext time.Time
	claims := 0
	for i := 0; i < 8; i++ {
		if _, err := platform.Exec(ctx,
			"UPDATE subscription_renewal_attempts SET next_attempt_at=now()-interval '1 minute' WHERE state='pending'"); err != nil {
			t.Fatal(err)
		}
		row, err := q.PlatformClaimRenewalAttempt(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if row.ID != fx.attemptID {
			t.Fatalf("claim %d grabbed attempt %d", i, row.ID)
		}
		claims++
		if err = q.PlatformFailRenewalAttempt(ctx,
			store.PlatformFailRenewalAttemptParams{ID: row.ID, LastError: "NO_SAVED_PAYMENT_METHOD"}); err != nil {
			t.Fatal(err)
		}
		var state string
		var attempts int
		if err := platform.QueryRow(ctx,
			"SELECT state, attempts, next_attempt_at FROM subscription_renewal_attempts WHERE id=$1",
			fx.attemptID).Scan(&state, &attempts, &lastNext); err != nil {
			t.Fatal(err)
		}
		if attempts != claims {
			t.Fatalf("attempt counter = %d, want %d", attempts, claims)
		}
		if claims < 5 {
			if state != "pending" {
				t.Fatalf("round %d: state %q, want pending", i, state)
			}
			want := time.Duration(1<<uint(claims-1)) * 24 * time.Hour
			delay := time.Until(lastNext)
			if delay < want-time.Hour || delay > want+time.Hour {
				t.Fatalf("round %d: backoff %v, want about %v", i, delay, want)
			}
		}
	}
	if claims != 5 {
		t.Fatalf("claims = %d, want exactly 5 before the attempt caps as failed", claims)
	}
	var state string
	var attempts int
	if err := platform.QueryRow(ctx,
		"SELECT state, attempts FROM subscription_renewal_attempts WHERE id=$1", fx.attemptID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || attempts != 5 {
		t.Fatalf("after 5 attempts state=%q attempts=%d, want failed/5", state, attempts)
	}
}

func TestRenewalFailureNotificationDedupesPerAttempt(t *testing.T) {
	ctx := context.Background()
	platform, _ := testdb.OpenMigrated(t)
	fx := newRenewalFixture(ctx, t, platform, "active")
	q := store.New(platform)

	if _, err := q.PlatformClaimRenewalAttempt(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := q.PlatformEnqueueRenewalFailure(ctx, fx.attemptID); err != nil {
			t.Fatal(err)
		}
	}
	if n := countRows(ctx, t, platform, "SELECT count(*) FROM notification_outbox WHERE kind='renewal_failed'"); n != 1 {
		t.Fatalf("notification rows = %d, want 1 for one attempt", n)
	}
	if _, err := platform.Exec(ctx,
		"UPDATE subscription_renewal_attempts SET attempts=2, next_attempt_at=now()-interval '1 minute' WHERE id=$1", fx.attemptID); err != nil {
		t.Fatal(err)
	}
	if err := q.PlatformEnqueueRenewalFailure(ctx, fx.attemptID); err != nil {
		t.Fatal(err)
	}
	if n := countRows(ctx, t, platform, "SELECT count(*) FROM notification_outbox WHERE kind='renewal_failed'"); n != 2 {
		t.Fatalf("notification rows = %d, want 2 after the second failed attempt", n)
	}
}

// BILL-002: due-renewal scheduling must honor the explicit auto-renew
// enrollment flag and the free-plan service waiver. A subscription whose
// owner turned auto-renew off (cancel_at_period_end=true) and a free-plan
// subscription must never gain a renewal charge task. Attempts are immutable
// history (no DELETE grant), so consumed fixture periods are canceled and the
// subscription moves to a fresh cycle key.
func TestEnqueueDueRenewalsHonorsOptOutAndFreePlan(t *testing.T) {
	ctx := context.Background()
	platform, _ := testdb.OpenMigrated(t)
	fx := newRenewalFixture(ctx, t, platform, "active")
	cancelFixtureAttempts := func() {
		t.Helper()
		if _, err := platform.Exec(ctx,
			"UPDATE subscription_renewal_attempts SET state='canceled' WHERE subscription_id=$1 AND state='pending'", fx.subscriptionID); err != nil {
			t.Fatal(err)
		}
	}
	shiftPeriod := func() {
		t.Helper()
		if _, err := platform.Exec(ctx,
			"UPDATE subscriptions SET period_end=period_end+interval '1 hour' WHERE id=$1", fx.subscriptionID); err != nil {
			t.Fatal(err)
		}
	}
	setFlag := func(enabled bool) {
		t.Helper()
		if _, err := platform.Exec(ctx,
			"UPDATE subscriptions SET cancel_at_period_end=$2 WHERE id=$1", fx.subscriptionID, enabled); err != nil {
			t.Fatal(err)
		}
	}
	setPlan := func(plan string) {
		t.Helper()
		if _, err := platform.Exec(ctx,
			"UPDATE subscriptions SET plan_id=$2 WHERE id=$1", fx.subscriptionID, plan); err != nil {
			t.Fatal(err)
		}
	}
	enqueue := func() {
		t.Helper()
		if err := store.New(platform).PlatformEnqueueDueRenewals(ctx); err != nil {
			t.Fatal(err)
		}
	}
	pendingAttempts := func() int64 {
		t.Helper()
		return countRows(ctx, t, platform,
			"SELECT count(*) FROM subscription_renewal_attempts WHERE subscription_id=$1 AND state='pending'", fx.subscriptionID)
	}
	cancelFixtureAttempts()
	shiftPeriod()
	// Free service entitlement: no payment debt may be scheduled.
	setPlan("free")
	enqueue()
	if n := pendingAttempts(); n != 0 {
		t.Fatalf("free plan scheduled %d renewal attempts, want 0", n)
	}
	// A paid subscription flag is not provider authorization or explicit consent.
	setPlan("renew-pro")
	setFlag(false)
	enqueue()
	if n := pendingAttempts(); n != 0 {
		t.Fatalf("unconsented paid plan scheduled %d renewal attempts, want 0", n)
	}
	// Opted out before the task fires: no new charge task may be scheduled.
	cancelFixtureAttempts()
	shiftPeriod()
	setFlag(true)
	enqueue()
	if n := pendingAttempts(); n != 0 {
		t.Fatalf("opted-out subscription scheduled %d renewal attempts, want 0", n)
	}
}

func TestSettleRenewalAttemptUncertainMarkerAndRetry(t *testing.T) {
	ctx := context.Background()
	platform, _ := testdb.OpenMigrated(t)
	fx := newRenewalFixture(ctx, t, platform, "active")
	q := store.New(platform)

	if _, err := q.PlatformClaimRenewalAttempt(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.PlatformMarkRenewalAttemptUncertain(ctx,
		store.PlatformMarkRenewalAttemptUncertainParams{ID: fx.attemptID, TenantID: fx.tenantID}); err != nil {
		t.Fatal(err)
	}
	var state, lastError string
	var attempts int
	if err := platform.QueryRow(ctx,
		"SELECT state, last_error, attempts FROM subscription_renewal_attempts WHERE id=$1",
		fx.attemptID).Scan(&state, &lastError, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "uncertain" || lastError != "PROVIDER_UNCERTAIN" {
		t.Fatalf("uncertain attempt: state=%q last_error=%q", state, lastError)
	}
	if attempts != 1 {
		t.Fatalf("uncertain marker must not consume an extra attempt: %d", attempts)
	}
	if _, err := billingrenew.PlatformSettleRenewalAttempt(ctx, platform, settleParams(fx, "txn_after_uncertain")); err != nil {
		t.Fatal(err)
	}
	if n := countRows(ctx, t, platform, "SELECT count(*) FROM payments WHERE provider_txn_id='txn_after_uncertain'"); n != 1 {
		t.Fatalf("payments = %d, want 1", n)
	}
}

// BILL-004: settling an order supersedes the old billing cycle. The pending
// attempt of the previous period is canceled inside the settlement
// transaction, the worker can no longer claim it (so it can never retry or
// notify again), and an attempt that escapes cancelation (enqueue/claim race)
// is refused by the claim guard because its period no longer matches the
// subscription's current billable cycle.
func TestSettleOrderSupersedesPendingRenewalAttemptsOfOldCycle(t *testing.T) {
	ctx := context.Background()
	platform, _ := testdb.OpenMigrated(t)
	fx := newRenewalFixture(ctx, t, platform, "active")
	q := store.New(platform)
	if _, err := platform.Exec(ctx,
		"INSERT INTO plan_prices(plan_code,currency,billing_cycle,amount_cents) VALUES('renew-pro','CNY','monthly',1000)"); err != nil {
		t.Fatal(err)
	}
	var adminID int64
	if err := platform.QueryRow(ctx,
		"INSERT INTO users(email,password_hash) VALUES('bill004-admin@example.test','unused') RETURNING id").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(ctx, "INSERT INTO platform_admins(user_id) VALUES($1)", adminID); err != nil {
		t.Fatal(err)
	}
	var orderID int64
	if err := platform.QueryRow(ctx,
		"INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code,billing_cycle)"+
			" VALUES($1,$2,'renew','pending',1000,'CNY','renew-pro','monthly') RETURNING id",
		fx.tenantID, fmt.Sprintf("BILL004-%d", fx.tenantID)).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	// The fixture subscription is paid-plan history without a ledger, so the
	// settlement gate would demand an audited calendar review; seed the frozen
	// calendar the fixture implies instead of exercising the legacy review.
	anchor := time.Now().UTC().Truncate(time.Microsecond)
	wall, _ := json.Marshal(map[string]any{"timezone": "UTC", "day": anchor.Day(), "hour": anchor.Hour(), "minute": anchor.Minute(), "second": anchor.Second(), "nanosecond": 0})
	if _, err := platform.Exec(ctx,
		"INSERT INTO billing_calendars(tenant_id,timezone,first_paid_at,anchor_at,anchor_wall,first_anchor_wall,source,review_reason)"+
			" VALUES($1,'UTC',$2,$2,$3,$3,'first_paid','fixture seed')",
		fx.tenantID, anchor, wall); err != nil {
		t.Fatal(err)
	}

	if err := handlers.SettleOrderWithDetails(ctx, platform, orderID, handlers.SettleOptions{
		Provider: "manual", AmountCents: 1000, Currency: "CNY", ActorUserID: adminID,
	}); err != nil {
		t.Fatalf("settle order: %v", err)
	}

	var state, lastError string
	if err := platform.QueryRow(ctx,
		"SELECT state,last_error FROM subscription_renewal_attempts WHERE id=$1", fx.attemptID).Scan(&state, &lastError); err != nil {
		t.Fatal(err)
	}
	if state != "canceled" || lastError != "SUPERSEDED_BY_PAYMENT" {
		t.Fatalf("old-cycle attempt after settlement: state=%q last_error=%q", state, lastError)
	}
	var oldStatus string
	if err := platform.QueryRow(ctx,
		"SELECT status FROM subscriptions WHERE id=$1", fx.subscriptionID).Scan(&oldStatus); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "canceled" {
		t.Fatalf("old subscription status = %q, want canceled", oldStatus)
	}

	// Due again: the worker path (enqueue + claim) must not touch it.
	if _, err := platform.Exec(ctx,
		"UPDATE subscription_renewal_attempts SET next_attempt_at=now()-interval '1 minute' WHERE id=$1", fx.attemptID); err != nil {
		t.Fatal(err)
	}
	worker := billingrenew.Worker{Store: q}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("worker run after settlement: %v", err)
	}
	if _, err := q.PlatformClaimRenewalAttempt(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("canceled attempt was claimed: err=%v", err)
	}
	if n := countRows(ctx, t, platform, "SELECT count(*) FROM notification_outbox WHERE kind='renewal_failed'"); n != 0 {
		t.Fatalf("canceled attempt produced %d failure notifications, want 0", n)
	}

	// Claim-level race guard: a stale pending attempt that escaped
	// cancelation is never claimed, because its period no longer matches the
	// subscription's current billable cycle.
	if _, err := platform.Exec(ctx,
		"UPDATE subscription_renewal_attempts SET state='pending',last_error='',next_attempt_at=now()-interval '1 minute' WHERE id=$1", fx.attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PlatformClaimRenewalAttempt(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale-cycle attempt was claimed: err=%v", err)
	}
	var staleState string
	var attempts int
	if err := platform.QueryRow(ctx,
		"SELECT state,attempts FROM subscription_renewal_attempts WHERE id=$1", fx.attemptID).Scan(&staleState, &attempts); err != nil {
		t.Fatal(err)
	}
	if staleState != "pending" || attempts != 0 {
		t.Fatalf("claim guard consumed the stale attempt: state=%q attempts=%d", staleState, attempts)
	}
}
