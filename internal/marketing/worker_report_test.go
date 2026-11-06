package marketing

import (
	"context"
	"errors"
	"sync"
	"testing"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

// An unsubscribe recorded between the claim and DATA stops the send
// immediately: reservation refunded, explicit reason recorded, never retried.
func TestUnsubscribeBetweenClaimAndSendIsSkippedNotRetried(t *testing.T) {
	f := marketingFixture(t, 5, 1)
	sends := 0
	worker := f.worker(t, func(c context.Context, _ SMTPConfig, d Delivery) error {
		sends++
		if _, err := store.New(f.platform.Pool).AddMarketingSuppression(c, store.AddMarketingSuppressionParams{TenantID: f.tenant, Email: d.Email, Reason: "unsubscribe"}); err != nil {
			return err
		}
		return d.BeforeData(c)
	})
	if _, err := worker.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state, code string
	if err := f.platform.QueryRow(context.Background(), "SELECT status,result_code FROM marketing_recipients WHERE tenant_id=$1", f.tenant).Scan(&state, &code); err != nil || state != "suppressed" || code != "unsubscribed_mid_run" {
		t.Fatalf("mid-run unsubscribe outcome: state=%s code=%s err=%v", state, code, err)
	}
	var used int64
	if err := f.platform.QueryRow(context.Background(), "SELECT COALESCE(sum(used_count),0) FROM marketing_quota_windows WHERE tenant_id=$1", f.tenant).Scan(&used); err != nil || used != 0 {
		t.Fatalf("reservation not refunded: %d %v", used, err)
	}
	// No blind retry: the recipient is terminal.
	if _, err := f.worker(t, func(context.Context, SMTPConfig, Delivery) error { sends++; return nil }).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sends != 1 {
		t.Fatalf("revoked recipient was retried: sends=%d", sends)
	}
}

// Two workers racing on the same window can never exceed the reservation:
// the advisory lock serializes the quota window increment.
func TestQuotaReservationRaceBetweenTwoWorkers(t *testing.T) {
	f := marketingFixture(t, 2, 4)
	var mu sync.Mutex
	order := []string{}
	sender := func(_ context.Context, _ SMTPConfig, d Delivery) error {
		mu.Lock()
		order = append(order, d.Email)
		mu.Unlock()
		return nil
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.worker(t, sender).Step(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var used int64
	if err := f.platform.QueryRow(context.Background(), "SELECT sum(used_count) FROM marketing_quota_windows WHERE tenant_id=$1", f.tenant).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != int64(len(order)) || used > 2 {
		t.Fatalf("reservations=%d sends=%d exceeded plan limit", used, len(order))
	}
}

// A permanent precondition failure at execution time skips the recipient with
// a recorded reason instead of lingering past the grace window.
func TestExecutionTimeEntitlementLossSkipsRecipient(t *testing.T) {
	f := marketingFixture(t, 5, 1)
	if _, err := f.platform.Exec(context.Background(), "DELETE FROM settings WHERE scope='tenant' AND scope_id=$1 AND key IN('marketing_daily_recipient_limit','marketing_rate_per_minute')", f.tenant); err != nil {
		t.Fatal(err)
	}
	sends := 0
	if _, err := f.worker(t, func(context.Context, SMTPConfig, Delivery) error { sends++; return nil }).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sends != 0 {
		t.Fatal("sent without entitlement")
	}
	var state, code string
	if err := f.platform.QueryRow(context.Background(), "SELECT status,result_code FROM marketing_recipients WHERE tenant_id=$1", f.tenant).Scan(&state, &code); err != nil || state != "canceled" || code != "quota_unconfigured" {
		t.Fatalf("skip reason not recorded: state=%s code=%s err=%v", state, code, err)
	}
}

// Outcome counters accumulate incrementally and are tenant-readable.
func TestOutcomeReportAccumulates(t *testing.T) {
	f := marketingFixture(t, 10, 3)
	ctx := context.Background()
	if _, err := store.New(f.platform.Pool).AddMarketingSuppression(ctx, store.AddMarketingSuppressionParams{TenantID: f.tenant, Email: "recipient-2@example.test", Reason: "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	sender := func(_ context.Context, _ SMTPConfig, d Delivery) error {
		if d.Email == "recipient-1@example.test" {
			return domainMail.MarkDeliveryUncertain(errors.New("connection lost after DATA"))
		}
		return nil
	}
	for range 3 {
		if _, err := f.worker(t, sender).Step(ctx); err != nil && !errors.Is(err, domainMail.ErrDeliveryUncertain) {
			t.Fatal(err)
		}
	}
	tx, err := f.app.UserTx(ctx, f.tenant, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	report, err := store.New(tx).GetMarketingCampaignReport(ctx, store.GetMarketingCampaignReportParams{TenantID: f.tenant, ID: f.campaign})
	if err != nil {
		t.Fatal(err)
	}
	if report.Sent != 1 || report.Uncertain != 1 || report.Unsubscribed != 1 || report.Failed != 0 || report.TotalRecipients != 3 || report.PendingRecipients != 0 {
		t.Fatalf("report mismatch: %+v", report)
	}
}
