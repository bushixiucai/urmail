package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestMarketingSuppressionsPersistAndRespectTenantRLS(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := context.Background()
	var brandID, tenantA, tenantB int64
	if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('suppression-test','Suppression') RETURNING id").Scan(&brandID); err != nil {
		t.Fatal(err)
	}
	for i, dest := range []*int64{&tenantA, &tenantB} {
		if err := platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,$2,$3,'active') RETURNING id", brandID, string(rune('A'+i)), string(rune('a'+i))).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	add := func(tid int64, email string) {
		t.Helper()
		tx, err := app.TenantTx(ctx, tid)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err = store.New(tx).AddMarketingSuppression(ctx, store.AddMarketingSuppressionParams{TenantID: tid, Email: email, Reason: mail.SuppressionReasonUnsubscribe}); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	add(tenantA, "person@example.test")
	add(tenantA, "person@example.test")
	// Hard-bounce suppression must survive later unsubscribe and manual writes/removal.
	tx, err := app.TenantTx(ctx, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(tx).AddMarketingSuppression(ctx, store.AddMarketingSuppressionParams{TenantID: tenantA, Email: "hardbounce@example.test", Reason: mail.SuppressionReasonBounce}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = app.TenantTx(ctx, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(tx).AddMarketingSuppression(ctx, store.AddMarketingSuppressionParams{TenantID: tenantA, Email: "hardbounce@example.test", Reason: mail.SuppressionReasonUnsubscribe}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = app.TenantTx(ctx, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(tx).RemoveMarketingSuppression(ctx, store.RemoveMarketingSuppressionParams{TenantID: tenantA, Email: "hardbounce@example.test"}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := platform.QueryRow(ctx, "SELECT reason FROM marketing_suppressions WHERE tenant_id=$1 AND email='hardbounce@example.test'", tenantA).Scan(&reason); err != nil || reason != mail.SuppressionReasonBounce {
		t.Fatalf("bounce was weakened/removed: reason=%q err=%v", reason, err)
	}
	// Unsubscribe consent is also stronger than a removable manual marker.
	tx, err = app.TenantTx(ctx, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(tx).AddMarketingSuppression(ctx, store.AddMarketingSuppressionParams{TenantID: tenantA, Email: "optout@example.test", Reason: mail.SuppressionReasonUnsubscribe}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = app.TenantTx(ctx, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(tx).AddMarketingSuppression(ctx, store.AddMarketingSuppressionParams{TenantID: tenantA, Email: "optout@example.test", Reason: mail.SuppressionReasonManual}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = app.TenantTx(ctx, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(tx).RemoveMarketingSuppression(ctx, store.RemoveMarketingSuppressionParams{TenantID: tenantA, Email: "optout@example.test"}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "SELECT reason FROM marketing_suppressions WHERE tenant_id=$1 AND email='optout@example.test'", tenantA).Scan(&reason); err != nil || reason != mail.SuppressionReasonUnsubscribe {
		t.Fatalf("unsubscribe consent was weakened/removed: reason=%q err=%v", reason, err)
	}
	for _, tc := range []struct {
		tenant int64
		want   bool
	}{{tenantA, true}, {tenantB, false}} {
		tx, err := app.TenantTx(ctx, tc.tenant)
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.New(tx).CheckMarketingSuppression(ctx, store.CheckMarketingSuppressionParams{TenantID: tc.tenant, Email: "person@example.test"})
		_ = tx.Rollback(ctx)
		if err != nil || got != tc.want {
			t.Fatalf("tenant %d suppression=%v want %v err=%v", tc.tenant, got, tc.want, err)
		}
	}
	manager, err := mail.NewSuppressionManager([]byte("integration-test-unsubscribe-secret"))
	if err != nil {
		t.Fatal(err)
	}
	token := manager.GenerateUnsubscribeToken(tenantB, "new@example.test")
	// Recreate the manager to model a process restart; stateless token verification must still work.
	restarted, err := mail.NewSuppressionManager([]byte("integration-test-unsubscribe-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if tid, email, ok := restarted.VerifyUnsubscribeToken(token); !ok || tid != tenantB || email != "new@example.test" {
		t.Fatalf("restart token verification tenant=%d email=%q ok=%v", tid, email, ok)
	}
	gin.SetMode(gin.TestMode)
	h := &MarketingSuppressionHandler{Store: store.New(app.Pool), Tokens: restarted, AppPool: app}
	r := gin.New()
	r.POST("/api/v1/marketing/unsubscribe", h.OneClick)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/marketing/unsubscribe?token="+token, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("unsubscribe HTTP status=%d body=%s", w.Code, w.Body.String())
		}
	}
	var count int64
	if err := platform.QueryRow(ctx, "SELECT count(*) FROM marketing_suppressions WHERE tenant_id=$1 AND email='new@example.test'", tenantB).Scan(&count); err != nil || count != 1 {
		t.Fatalf("idempotent unsubscribe rows=%d err=%v", count, err)
	}
	tx, err = app.TenantTx(ctx, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	checker := mail.NewPersistentSuppressionChecker(store.New(tx))
	if suppressed, checkErr := checker.IsSuppressed(ctx, tenantA, "person@example.test"); checkErr != nil || !suppressed {
		_ = tx.Rollback(ctx)
		t.Fatalf("durable checker suppressed=%v err=%v", suppressed, checkErr)
	}
	if suppressed, checkErr := checker.IsSuppressed(ctx, tenantA, "clear@example.test"); checkErr != nil || suppressed {
		_ = tx.Rollback(ctx)
		t.Fatalf("durable checker unexpected suppression=%v err=%v", suppressed, checkErr)
	}
	_ = tx.Rollback(ctx)
	if app.Role() != db.RoleApp {
		t.Fatalf("expected tenant app role, got %s", app.Role())
	}
}
