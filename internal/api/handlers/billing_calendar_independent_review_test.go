package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestCalendarReviewRequiresLegacyFactsEvenWithoutTermLedger(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	// A pre-ledger paid subscription cannot establish historical timezone from current settings.
	if _, err := s.p.Exec(ctx, "UPDATE subscriptions SET plan_id='high',status='active',period_start='2024-01-31T01:00:00Z',period_end='2099-12-31T01:00:00Z',plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='high') WHERE tenant_id=$1", s.tid); err != nil {
		t.Fatal(err)
	}
	var order int64
	if err := s.p.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code,billing_cycle) VALUES($1,'LEGACY-CALENDAR-PENDING','renew','pending',20000,'CNY','high','monthly') RETURNING id", s.tid).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if _, err := s.p.Exec(ctx, "INSERT INTO platform_admins(user_id) VALUES($1)", s.uid); err != nil {
		t.Fatal(err)
	}
	err := SettleOrderWithDetails(ctx, s.p, order, SettleOptions{Provider: "manual", AmountCents: 20000, Currency: "CNY", ActorUserID: s.uid})
	var count int
	if queryErr := s.p.QueryRow(ctx, "SELECT count(*) FROM billing_calendars WHERE tenant_id=$1", s.tid).Scan(&count); queryErr != nil {
		t.Fatal(queryErr)
	}
	if err == nil || count != 0 {
		t.Fatalf("unreviewed legacy paid subscription invented calendar: settle=%v calendars=%d", err, count)
	}
}

func TestCalendarReviewCurrentRefundDoesNotMakeFuturePaidRenewalUnfulfillable(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	first := s.pay(t, "high", "calendar-current")
	future := s.pay(t, "high", "calendar-future")
	var futureEnd time.Time
	if err := s.p.QueryRow(ctx, "SELECT period_end FROM subscription_terms WHERE tenant_id=$1 AND order_id=$2", s.tid, future.ID).Scan(&futureEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := s.p.Exec(ctx, "UPDATE subscription_terms SET revoked_at=now() WHERE tenant_id=$1 AND order_id=$2", s.tid, first.ID); err != nil {
		t.Fatal(err)
	}
	response := s.post(t, "/quote", `{"plan_code":"high","billing_cycle":"monthly"}`, "")
	if response.Code != 200 {
		t.Fatalf("future paid renewal should remain purchasable: %d %s", response.Code, response.Body.String())
	}
	var quote billing.Quote
	if err := json.Unmarshal(response.Body.Bytes(), &quote); err != nil {
		t.Fatal(err)
	}
	created := s.post(t, "/checkout", `{"quote":`+response.Body.String()+`}`, "calendar-gap-purchase")
	if created.Code != 201 {
		t.Fatalf("future paid checkout %d %s", created.Code, created.Body.String())
	}
	var body struct {
		Order store.Orders `json:"order"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	t.Logf("gap purchase quote kind=%s periods=%s..%s", quote.Kind, quote.PeriodStart, quote.PeriodEnd)
	err := SettleOrder(ctx, s.p, body.Order.ID)
	var status string
	if readErr := s.p.QueryRow(ctx, "SELECT status FROM orders WHERE tenant_id=$1 AND id=$2", s.tid, body.Order.ID).Scan(&status); readErr != nil {
		t.Fatal(readErr)
	}
	t.Logf("gap settlement status=%s err=%v", status, err)
	if quote.Kind != "renew" || !quote.PeriodStart.Equal(futureEnd) || status != "paid" {
		t.Fatalf("future prepaid timeline lost: kind=%s start=%s futureEnd=%s status=%s", quote.Kind, quote.PeriodStart, futureEnd, status)
	}
	if err != nil {
		t.Fatalf("accepted quote overlaps future paid rights instead of refusing or safely fulfilling: quoteKind=%s period=%s..%s settle=%v", quote.Kind, quote.PeriodStart.Format(time.RFC3339Nano), quote.PeriodEnd.Format(time.RFC3339Nano), err)
	}
}

func TestCalendarReviewDoesNotForgetCanceledLegacyPaidHistory(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	// Cancellation does not prove there was never paid service or a frozen timezone.
	if _, err := s.p.Exec(ctx, "UPDATE subscriptions SET plan_id='high',status='canceled',period_start='2024-01-31T01:00:00Z',period_end='2024-02-29T01:00:00Z',plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='high') WHERE tenant_id=$1", s.tid); err != nil {
		t.Fatal(err)
	}
	response := s.post(t, "/quote", `{"plan_code":"high","billing_cycle":"monthly"}`, "")
	if response.Code != 409 {
		t.Fatalf("canceled legacy paid history silently reset first timezone: %d %s", response.Code, response.Body.String())
	}
}

func TestCalendarReviewFirstPaidAuditFailureRollsBackAllCalendarRights(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	quoted := s.post(t, "/quote", `{"plan_code":"high","billing_cycle":"monthly"}`, "")
	if quoted.Code != 200 {
		t.Fatalf("quote: %s", quoted.Body.String())
	}
	created := s.post(t, "/checkout", `{"quote":`+quoted.Body.String()+`}`, "calendar-audit-retry")
	if created.Code != 201 {
		t.Fatalf("checkout: %s", created.Body.String())
	}
	var body struct {
		Order store.Orders `json:"order"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, err := s.p.Exec(ctx, "CREATE FUNCTION calendar_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'BILLING_CALENDAR %' THEN RAISE EXCEPTION 'injected calendar audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER calendar_fault BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION calendar_fault()"); err != nil {
		t.Fatal(err)
	}
	if err := SettleOrder(ctx, s.p, body.Order.ID); err == nil {
		t.Fatal("mandatory calendar audit fault was ignored")
	}
	for _, table := range []string{"billing_calendars", "subscription_terms", "payments", "invoices"} {
		var count int
		if err := s.p.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE tenant_id=$1", s.tid).Scan(&count); err != nil || count != 0 {
			t.Fatalf("calendar audit failure leaked %s=%d err=%v", table, count, err)
		}
	}
	var status string
	if err := s.p.QueryRow(ctx, "SELECT status FROM orders WHERE tenant_id=$1 AND id=$2", s.tid, body.Order.ID).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("failed first service status %s err=%v", status, err)
	}
	if _, err := s.p.Exec(ctx, "DROP TRIGGER calendar_fault ON audit_logs; DROP FUNCTION calendar_fault()"); err != nil {
		t.Fatal(err)
	}
	if err := SettleOrder(ctx, s.p, body.Order.ID); err != nil {
		t.Fatal("calendar audit recovery failed", err)
	}
	var count int
	if err := s.p.QueryRow(ctx, "SELECT count(*) FROM billing_calendars WHERE tenant_id=$1", s.tid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("calendar established %d times: %v", count, err)
	}
}

func TestCalendarReviewBareTrialMarkerCannotInventHistoricalFacts(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	if _, err := s.p.Exec(ctx, "UPDATE subscriptions SET plan_id='high',status='trialing',period_start='2024-01-01T00:00:00Z',period_end='2024-01-08T00:00:00Z',trial_ends_at='2024-01-08T00:00:00Z',plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='high') WHERE tenant_id=$1", s.tid); err != nil {
		t.Fatal(err)
	}
	response := s.post(t, "/quote", `{"plan_code":"high","billing_cycle":"monthly"}`, "")
	if response.Code != 409 {
		t.Fatalf("unprotected trial marker substituted for immutable trial evidence: %d %s", response.Code, response.Body.String())
	}
}

func TestCalendarReviewProtectedExpiredTrialAllowsFirstActualPaidService(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	if _, err := s.p.Exec(ctx, "INSERT INTO coupons(code,kind,value) VALUES('calendar-proven-trial','trial_days',7)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.p.Exec(ctx, "INSERT INTO tenant_trials(tenant_id,domain_id,coupon_code,plan_code,plan_snapshot,started_at,ends_at,redeemed_by) SELECT $1,d.id,'calendar-proven-trial','high',to_jsonb(p),'2024-01-01T00:00:00Z','2024-01-08T00:00:00Z',$2 FROM domains d JOIN plans p ON p.code='high' WHERE d.tenant_id=$1 LIMIT 1", s.tid, s.uid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.p.Exec(ctx, "UPDATE subscriptions SET plan_id='high',status='canceled',period_start='2024-01-01T00:00:00Z',period_end='2024-01-08T00:00:00Z',trial_ends_at='2024-01-08T00:00:00Z',plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='high') WHERE tenant_id=$1", s.tid); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"UPDATE tenant_trials SET ends_at=ends_at+interval '1 day' WHERE tenant_id=$1", "UPDATE subscriptions SET trial_ends_at=trial_ends_at+interval '1 day' WHERE tenant_id=$1"} {
		tx, err := s.a.UserTx(ctx, s.tid, s.uid)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, statement, s.tid)
		_ = tx.Rollback(ctx)
		if err == nil {
			t.Fatal("application forged protected trial evidence")
		}
	}
	if _, err := s.p.Exec(ctx, "UPDATE subscriptions SET trial_ends_at='2024-01-09T00:00:00Z' WHERE tenant_id=$1", s.tid); err != nil {
		t.Fatal(err)
	}
	if rejected := s.post(t, "/quote", `{"plan_code":"high","billing_cycle":"monthly"}`, ""); rejected.Code != 409 {
		t.Fatalf("mismatched protected trial tuple accepted: %d", rejected.Code)
	}
	if _, err := s.p.Exec(ctx, "UPDATE subscriptions SET trial_ends_at='2024-01-08T00:00:00Z' WHERE tenant_id=$1", s.tid); err != nil {
		t.Fatal(err)
	}
	response := s.post(t, "/quote", `{"plan_code":"high","billing_cycle":"monthly"}`, "")
	if response.Code != 200 {
		t.Fatalf("protected expired trial wrongly demanded invented paid history: %d %s", response.Code, response.Body.String())
	}
	var quote billing.Quote
	if err := json.Unmarshal(response.Body.Bytes(), &quote); err != nil {
		t.Fatal(err)
	}
	if quote.Calendar == nil || !quote.Calendar.Provisional || quote.Calendar.Version != 0 {
		t.Fatalf("trial became a paid calendar: %+v", quote.Calendar)
	}
	created := s.post(t, "/checkout", `{"quote":`+response.Body.String()+`}`, "calendar-first-after-proven-trial")
	if created.Code != 201 {
		t.Fatalf("first paid after trial checkout %d %s", created.Code, created.Body.String())
	}
	var body struct {
		Order store.Orders `json:"order"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if err := SettleOrder(ctx, s.p, body.Order.ID); err != nil {
		t.Fatal(err)
	}
	var actual time.Time
	if err := s.p.QueryRow(ctx, "SELECT first_paid_at FROM billing_calendars WHERE tenant_id=$1", s.tid).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual.Before(time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("trial beginning was mistaken for first paid service: %s", actual)
	}
}
