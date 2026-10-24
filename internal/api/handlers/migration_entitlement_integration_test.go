package handlers

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"

	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestMigrationEntitlementConsumersAndFirstCommercialPurchase(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.p.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", s.uid)
	grant, err := store.New(s.p).PlatformGrantMigrationEntitlement(ctx, store.PlatformGrantMigrationEntitlementParams{TenantID: s.tid, ActorID: s.uid, ManifestSha256: strings.Repeat("b", 64), Reason: "Reviewed isolated legacy source manifest and confirmed existing mail service"})
	if err != nil {
		t.Fatal(err)
	}
	if grant.PeriodEnd.Valid || !serviceEntitlement(grant).CurrentAt(time.Now().AddDate(200, 0, 0)) {
		t.Fatal("migration grant is not genuinely non-expiring")
	}
	for _, path := range []string{"/cancel", "/change"} {
		w := s.post(t, path, `{"plan_code":"high"}`, "")
		if w.Code != 409 || !strings.Contains(w.Body.String(), "MIGRATION_ENTITLEMENT_PRESERVED") {
			t.Fatalf("%s %d %s", path, w.Code, w.Body)
		}
	}

	platformHandler := &PlatformHandler{Pool: s.p, Store: store.New(s.p)}
	platformRouter := gin.New()
	platformRouter.Use(middleware.AuditPlatform(store.New(s.p), zap.NewNop()), func(c *gin.Context) { c.Set(middleware.ContextUserID, s.uid) }, middleware.RequirePlatformAdmin(store.New(s.p)))
	platformRouter.POST("/api/v1/platform/tenants/:id/subscription", middleware.RequirePlatformTx(s.p, zap.NewNop()), platformHandler.AssignSubscription)
	assignResponse := httptest.NewRecorder()
	assignRequest := httptest.NewRequestWithContext(ctx, "POST", fmt.Sprintf("/api/v1/platform/tenants/%d/subscription", s.tid), strings.NewReader(`{"plan_code":"high"}`))
	assignRequest.Header.Set("Content-Type", "application/json")
	platformRouter.ServeHTTP(assignResponse, assignRequest)
	if assignResponse.Code != 409 || !strings.Contains(assignResponse.Body.String(), "MIGRATION_ENTITLEMENT_PRESERVED") {
		t.Fatalf("default manual assignment replaced preserved service %d %s", assignResponse.Code, assignResponse.Body)
	}
	tx, err := s.a.UserTx(ctx, s.tid, s.uid)
	if err != nil {
		t.Fatal(err)
	}
	q := store.New(tx)
	plan, err := q.GetEffectivePlan(ctx, s.tid)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Active || plan.QuotaBytes != -1 || plan.DailySendLimit != -1 || strings.Contains(string(plan.Features), "ai_") || strings.Contains(string(plan.Features), "marketing_") {
		t.Fatalf("hidden mail-only plan %#v", plan)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(ctx, "POST", "/mailboxes", nil)
	if !quotaCheck(c, q, s.tid, "mailbox") || !quotaCheck(c, q, s.tid, "domain") {
		t.Fatal("HTTP quota refused preserved mail service")
	}
	_ = tx.Rollback(ctx)
	quoteResp := s.post(t, "/quote", `{"plan_code":"high","billing_cycle":"monthly"}`, "")
	if quoteResp.Code != 200 {
		t.Fatalf("first commercial quote %d %s", quoteResp.Code, quoteResp.Body)
	}
	var quote billing.Quote
	if err = json.Unmarshal(quoteResp.Body.Bytes(), &quote); err != nil {
		t.Fatal(err)
	}
	if quote.CurrentEntitlementSource != billing.EntitlementMigration || quote.Kind != "new" || quote.Calendar == nil || !quote.Calendar.Provisional || quote.PeriodStart.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("first purchase inherited legacy period or paid facts %#v", quote)
	}
	oldSource := quote
	oldSource.CurrentEntitlementSource = ""
	oldSource.Digest = oldSource.ComputeDigest([]byte("timeline-fixture-key-with-at-least-thirty-two-bytes"))
	payload, _ := json.Marshal(oldSource)
	oldTx, oldErr := s.a.UserTx(ctx, s.tid, s.uid)
	if oldErr != nil {
		t.Fatal(oldErr)
	}
	oldQ := store.New(oldTx)
	stored, oldErr := oldQ.GetBillingQuote(ctx, store.GetBillingQuoteParams{TenantID: s.tid, Digest: quote.Digest})
	if oldErr != nil {
		t.Fatal(oldErr)
	}
	if oldErr = oldQ.SaveBillingQuote(ctx, store.SaveBillingQuoteParams{Digest: oldSource.Digest, TenantID: s.tid, Payload: payload, PlanSnapshot: stored.PlanSnapshot, DocumentSnapshot: stored.DocumentSnapshot, ExpiresAt: pgtype.Timestamptz{Time: oldSource.ExpiresAt, Valid: true}}); oldErr != nil {
		t.Fatal(oldErr)
	}
	if oldErr = oldTx.Commit(ctx); oldErr != nil {
		t.Fatal(oldErr)
	}

	rejected := s.post(t, "/checkout", `{"quote":`+string(payload)+`}`, "missing-source")
	if rejected.Code != 409 {
		t.Fatalf("missing-source quote accepted%d %s", rejected.Code, rejected.Body)
	}
	checkout := s.post(t, "/checkout", `{"quote":`+quoteResp.Body.String()+`}`, "migration-first-paid")
	if checkout.Code != 201 {
		t.Fatalf("first commercial checkout %d %s", checkout.Code, checkout.Body)
	}
	var result struct {
		Order store.Orders `json:"order"`
	}
	if err = json.Unmarshal(checkout.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	lateCheckout := s.post(t, "/checkout", `{"quote":`+quoteResp.Body.String()+`}`, "migration-late-paid")
	if lateCheckout.Code != 201 {
		t.Fatalf("second durable intent %d %s", lateCheckout.Code, lateCheckout.Body)
	}
	var late struct {
		Order store.Orders `json:"order"`
	}
	if err = json.Unmarshal(lateCheckout.Body.Bytes(), &late); err != nil {
		t.Fatal(err)
	}
	if err = SettleOrder(ctx, s.p, result.Order.ID); err != nil {
		t.Fatal(err)
	}
	current, err := store.New(s.p).PlatformGetSubscriptionForSettlement(ctx, s.tid)
	if err != nil {
		t.Fatal(err)
	}
	if current.EntitlementSource != billing.EntitlementStandard || current.MigrationManifestSha256.Valid || !current.PeriodEnd.Valid || current.PeriodStart.Time.Before(time.Now().Add(-time.Minute)) || current.PeriodEnd.Time.After(time.Now().AddDate(0, 2, 0)) {
		t.Fatalf("paid conversion invalid %#v", current)
	}
	if err = SettleOrderWithDetails(ctx, s.p, late.Order.ID, SettleOptions{Provider: payment.ProviderStripe, ProviderTxnID: "migration-source-stale-cash", AmountCents: late.Order.AmountCents, Currency: late.Order.Currency}); err != nil {
		t.Fatal(err)
	}
	var review int
	if err = s.p.QueryRow(ctx, "SELECT count(*) FROM payment_manual_reviews WHERE order_id=$1 AND reason='entitlement_source_changed'", late.Order.ID).Scan(&review); err != nil || review != 1 {
		t.Fatalf("stale migration cash did not become manual review %d %v", review, err)
	}
	replay, err := store.New(s.p).PlatformGrantMigrationEntitlement(ctx, store.PlatformGrantMigrationEntitlementParams{TenantID: s.tid, ActorID: s.uid, ManifestSha256: strings.Repeat("b", 64), Reason: "Reviewed same manifest again without changing any commercial entitlement"})
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != grant.ID || replay.Status != "canceled" {
		t.Fatal("reimport reactivated legacy grant")
	}
	var count int
	if err = s.p.QueryRow(ctx, "SELECT count(*) FROM subscriptions WHERE tenant_id=$1 AND status='active' AND entitlement_source='standard'", s.tid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("paid entitlement changed after replay %d %v", count, err)
	}
}
