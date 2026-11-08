package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

func providerBudgetAdmin(t *testing.T, pool *db.Pool, tenant int64) int64 {
	t.Helper()
	var actor int64
	if err := pool.QueryRow(t.Context(), "SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner'", tenant).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "INSERT INTO platform_admins(user_id)VALUES($1)", actor); err != nil {
		t.Fatal(err)
	}
	return actor
}
func providerBudgetPut(t *testing.T, pool *db.Pool, tenant, actor int64) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextUserID, actor)
		c.Set(middleware.ContextTenantID, tenant+1000)
		c.Next()
	})
	router.POST("/tenants/:id/ai/budget", (&AIConfigurationHandler{Pool: pool}).PlatformBudgetPut)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, fmt.Sprintf("/tenants/%d/ai/budget", tenant), strings.NewReader(`{"daily_token_limit":1}`))
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	router.ServeHTTP(result, request)
	return result
}
func waitProviderBudgetWaiters(t *testing.T, pool *db.Pool, tenant int64, count int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var actual int
		err := pool.QueryRow(t.Context(), "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND objid::bigint=$1", tenant).Scan(&actual)
		if err != nil {
			t.Fatal(err)
		}
		if actual >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("target-tenant admission lock did not serialize queued operations")
}
func TestAIProviderBudgetPutOrdersBeforeQueuedNativeAdmissionPG(t *testing.T) {
	pool, principal, model, service, target, input := providerBudgetFixture(t, "platform", "classify")
	actor := providerBudgetAdmin(t, pool, principal.TenantID)
	blocker, err := pool.PlatformTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if err = store.New(blocker).PlatformLockBillingTenant(t.Context(), principal.TenantID); err != nil {
		t.Fatal(err)
	}
	put := make(chan *httptest.ResponseRecorder, 1)
	go func() { put <- providerBudgetPut(t, pool, principal.TenantID, actor) }()
	waitProviderBudgetWaiters(t, pool, principal.TenantID, 1)
	run := make(chan error, 1)
	go func() {
		_, _, e := service.Run(t.Context(), principal, "budget-put-queued-native", input, target)
		run <- e
	}()
	waitProviderBudgetWaiters(t, pool, principal.TenantID, 2)
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if response := <-put; response.Code != 204 {
		t.Fatal("budget update failed", response.Code, response.Body)
	}
	if err = <-run; !errors.Is(err, errAIBudget) {
		t.Fatal("queued native used superseded budget", err)
	}
	rows, err := store.New(pool).PlatformAIUsage(t.Context(), principal.TenantID)
	if err != nil || len(rows) != 0 || model.calls.Load() != 0 {
		t.Fatal("lowered budget reached provider or reserved usage", len(rows), model.calls.Load(), err)
	}
	var audits int
	if err = pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND actor_user_id=$2 AND action='UPDATE ai.budget'", principal.TenantID, actor).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("budget update missing atomic target audit", audits, err)
	}
}
func TestAIProviderBudgetPutRechecksQueuedPlatformActorPG(t *testing.T) {
	for _, mode := range []string{"revoked", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			pool, principal, _, _, _, _ := providerBudgetFixture(t, "platform", "classify")
			actor := providerBudgetAdmin(t, pool, principal.TenantID)
			blocker, err := pool.PlatformTx(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			if err = store.New(blocker).PlatformLockBillingTenant(t.Context(), principal.TenantID); err != nil {
				t.Fatal(err)
			}
			put := make(chan *httptest.ResponseRecorder, 1)
			go func() { put <- providerBudgetPut(t, pool, principal.TenantID, actor) }()
			waitProviderBudgetWaiters(t, pool, principal.TenantID, 1)
			if mode == "revoked" {
				_, err = pool.Exec(t.Context(), "DELETE FROM platform_admins WHERE user_id=$1", actor)
			} else {
				_, err = pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", actor)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if response := <-put; response.Code != 403 {
				t.Fatal("stale platform actor changed budget", response.Code, response.Body)
			}
			config, e := store.New(pool).PlatformGetAIProvider(t.Context(), principal.TenantID)
			if e != nil || config.DailyTokenLimit != 0 {
				t.Fatal("rejected update persisted", config.DailyTokenLimit, e)
			}
		})
	}
}
func TestAIProviderBudgetPutRollsBackWhenAuditFailsPG(t *testing.T) {
	pool, principal, _, _, _, _ := providerBudgetFixture(t, "platform", "classify")
	actor := providerBudgetAdmin(t, pool, principal.TenantID)
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION refuse_ai_budget_audit()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='UPDATE ai.budget' THEN RAISE EXCEPTION 'controlled audit refusal';END IF;RETURN NEW;END$$;CREATE TRIGGER refuse_ai_budget_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION refuse_ai_budget_audit()`); err != nil {
		t.Fatal(err)
	}
	response := providerBudgetPut(t, pool, principal.TenantID, actor)
	if response.Code != 503 {
		t.Fatal("failed audit published update success", response.Code, response.Body)
	}
	config, err := store.New(pool).PlatformGetAIProvider(t.Context(), principal.TenantID)
	if err != nil || config.DailyTokenLimit != 0 {
		t.Fatal("unaudited budget survived rollback", config.DailyTokenLimit, err)
	}
}
