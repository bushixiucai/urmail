package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

func aiQuotaPut(t *testing.T, pool *db.Pool, tenant, actor int64, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextUserID, actor)
		c.Set(middleware.ContextTenantID, tenant)
		c.Next()
	})
	router.POST("/tenants/:id/ai/budget", (&AIConfigurationHandler{Pool: pool}).PlatformBudgetPut)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, fmt.Sprintf("/tenants/%d/ai/budget", tenant), bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
func aiQuotaCaps(calls int64) map[string]any {
	return map[string]any{"nonplatform_call_limit": calls, "nonplatform_mailbox_call_limit": calls, "nonplatform_concurrency_limit": int64(2)}
}
func aiQuotaLegacyFixture(t *testing.T) (*db.Pool, store.PlatformConsumeMailboxTicketRow, *providerBudgetModel, *MailboxAIService, int64) {
	t.Helper()
	pool, principal, model, service, _, _ := providerBudgetFixtureUnseeded(t, "byo", "classify")
	actor := providerBudgetAdmin(t, pool, principal.TenantID)
	return pool, principal, model, service, actor
}
func aiQuotaUsage(t *testing.T, pool *db.Pool, principal store.PlatformConsumeMailboxTicketRow) store.PlatformAIQuotaUsageRow {
	t.Helper()
	q := store.New(pool)
	cfg, err := q.PlatformGetAIProvider(t.Context(), principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	quota, err := resolveAIQuota(t.Context(), q, cfg, principal.TenantID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	usage, err := q.PlatformAIQuotaUsage(t.Context(), store.PlatformAIQuotaUsageParams{TenantID: principal.TenantID, MailboxID: principal.MailboxID, IndependentWindow: quota.Independent, PeriodStart: quota.Start, PeriodEnd: quota.End})
	if err != nil {
		t.Fatal(err)
	}
	return usage
}
func TestAINonplatformQuotaLegacyNativeAndQAKeepMailFactsPG(t *testing.T) {
	pool, principal, model, service, actor := aiQuotaLegacyFixture(t)
	var before, after string
	query := "SELECT status::text || ':' || (SELECT count(*)::text FROM mailboxes WHERE tenant_id=tenants.id) FROM tenants WHERE id=$1"
	if err := pool.QueryRow(t.Context(), query, principal.TenantID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	target := mailboxAIVersion(t, pool, principal.TenantID)
	input := ai.OperationInput{Operation: "classify", Body: "Owned synthetic legacy message"}
	if _, err := service.Run(t.Context(), principal, "legacy-no-config-quota", input, target); !errors.Is(err, errAIBudget) || model.calls.Load() != 0 {
		t.Fatal("legacy AI opened without quota", err)
	}
	body := aiQuotaCaps(3)
	body["daily_token_limit"] = 100000
	body["daily_cost_micros"] = 1
	response := aiQuotaPut(t, pool, principal.TenantID, actor, body)
	if response.Code != 204 {
		t.Fatal(response.Code, response.Body)
	}
	// Old platform spending must neither consume BYO calls nor block a BYO
	// request whose platform-model cost reservation is zero.
	if _, err := pool.Exec(t.Context(), `INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,provider,model,cost_currency,input_rate,output_rate,status,reserved_tokens,reserved_cost_micros,actual_cost_micros) VALUES($1,$2,'old-platform-cost-fixture',repeat('b',64),now()-interval '2 days',now()+interval '30 days','platform','openai','old-platform','CNY',1,1,'completed',1,1000,1000)`, principal.TenantID, principal.MailboxID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(t.Context(), principal, "legacy-configured-classify", input, target); err != nil {
		t.Fatal(err)
	}
	model.operation = "draft_reply"
	input.Operation = "draft_reply"
	if _, err := service.Run(t.Context(), principal, "legacy-configured-reply", input, target); err != nil {
		t.Fatal(err)
	}
	reservation, err := (AISearchAskAccounting{Pool: pool}).Reserve(t.Context(), principal.TenantID, principal.MailboxID, "Synthetic grounded question")
	if err != nil || reservation == nil {
		t.Fatal("legacy QA has no independent window", err)
	}
	if _, err = service.Run(t.Context(), principal, "legacy-configured-over-cap", input, target); !errors.Is(err, errAIBudget) {
		t.Fatal("legacy over-cap operation allowed", err)
	}
	usage := aiQuotaUsage(t, pool, principal)
	if usage.CallsUsed != 3 || usage.MailboxCallsUsed != 3 || model.calls.Load() != 2 {
		t.Fatal("quota ledger is not shared", usage, model.calls.Load())
	}
	if err = pool.QueryRow(t.Context(), query, principal.TenantID).Scan(&after); err != nil || after != before {
		t.Fatal("AI configuration changed immutable mail entitlement", err)
	}
}
func TestAINonplatformQuotaHistoryModeTimezoneAndDisableCannotResetPG(t *testing.T) {
	pool, principal, model, service, actor := aiQuotaLegacyFixture(t)
	if _, err := pool.Exec(t.Context(), `INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,provider,model,cost_currency,input_rate,output_rate,status,reserved_tokens,reserved_cost_micros) VALUES($1,$2,'old-finite-byo-history-key',repeat('b',64),now()-interval '2 days',now()+interval '30 days','byo','openai','old-byo','CNY',0,0,'uncertain',100,0)`, principal.TenantID, principal.MailboxID); err != nil {
		t.Fatal(err)
	}
	if response := aiQuotaPut(t, pool, principal.TenantID, actor, aiQuotaCaps(2)); response.Code != 204 {
		t.Fatal(response.Code, response.Body)
	}
	cfg, err := store.New(pool).PlatformGetAIProvider(t.Context(), principal.TenantID)
	if err != nil || cfg.QuotaTimezone != "Asia/Shanghai" {
		t.Fatal("default business timezone was not frozen", cfg.QuotaTimezone, err)
	}
	input := ai.OperationInput{Operation: "classify", Body: "Synthetic message"}
	target := mailboxAIVersion(t, pool, principal.TenantID)
	if _, err = service.Run(t.Context(), principal, "legacy-one-new-after-old-row", input, target); err != nil {
		t.Fatal(err)
	}
	for _, body := range []map[string]any{{"nonplatform_call_limit": 0, "nonplatform_mailbox_call_limit": 0, "nonplatform_concurrency_limit": 0}, aiQuotaCaps(2)} {
		if response := aiQuotaPut(t, pool, principal.TenantID, actor, body); response.Code != 204 {
			t.Fatal(response.Code, response.Body)
		}
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO settings(scope,scope_id,key,value)VALUES('tenant',$1,'business_timezone','"America/New_York"') ON CONFLICT(scope,scope_id,key)DO UPDATE SET value=EXCLUDED.value`, principal.TenantID); err != nil {
		t.Fatal(err)
	}
	// Restoring a replay epoch cannot mint another AI quota window.
	if _, err = pool.Exec(t.Context(), `INSERT INTO settings(scope,scope_id,key,value)VALUES('platform',0,'restore_replay_review','{"sync_epoch":3}') ON CONFLICT(scope,scope_id,key)DO UPDATE SET value=EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), "UPDATE ai_provider_configs SET enabled=false WHERE tenant_id=$1", principal.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), "UPDATE ai_provider_configs SET enabled=true WHERE tenant_id=$1", principal.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Run(t.Context(), principal, "legacy-reenabled-no-refund", input, target); !errors.Is(err, errAIBudget) {
		t.Fatal("disable/re-enable returned spent quota", err)
	}
	if _, err = store.New(pool).PlatformSetAIProvider(t.Context(), store.PlatformSetAIProviderParams{TenantID: principal.TenantID, Mode: "local", Provider: "ollama", Endpoint: "https://127.0.0.1:11434", Model: "owned-local", Enabled: true, LocalOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Run(t.Context(), principal, "local-mode-no-new-window", input, mailboxAIVersion(t, pool, principal.TenantID)); !errors.Is(err, errAIBudget) {
		t.Fatal("switching BYO/local returned quota", err)
	}
	usage := aiQuotaUsage(t, pool, principal)
	if usage.CallsUsed != 2 || model.calls.Load() != 1 {
		t.Fatal("historical finite usage disappeared", usage, model.calls.Load())
	}
	cfg, err = store.New(pool).PlatformGetAIProvider(t.Context(), principal.TenantID)
	if err != nil || cfg.QuotaTimezone != "Asia/Shanghai" {
		t.Fatal("business setting changed frozen quota timezone", cfg.QuotaTimezone, err)
	}
	if _, err = pool.Exec(t.Context(), "UPDATE ai_provider_configs SET quota_timezone='America/New_York' WHERE tenant_id=$1", principal.TenantID); err == nil {
		t.Fatal("frozen quota timezone could be changed")
	}
}
func TestAINonplatformQuotaFiniteFallbackAndPlatformIsolationPG(t *testing.T) {
	pool, principal, model, service, target, input := providerBudgetFixture(t, "byo", "classify")
	if _, err := pool.Exec(t.Context(), `INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',$1,'ai_default_call_limit','10') ON CONFLICT(scope,scope_id,key) DO UPDATE SET value=EXCLUDED.value`, principal.TenantID); err != nil {
		t.Fatal(err)
	}
	quota, err := resolveAIQuota(t.Context(), store.New(pool), mustAIQuotaConfig(t, pool, principal.TenantID), principal.TenantID, time.Now())
	if err != nil || quota.Source != "configured_nonplatform" || !quota.Independent || quota.Limits.AI.Calls != 10 {
		t.Fatal("settings ai_default_call_limit fallback refused", quota.Source, err)
	}
	if _, err = service.Run(t.Context(), principal, "finite-byo-compatible-key", input, target); err != nil {
		t.Fatal(err)
	}
	if model.calls.Load() != 1 {
		t.Fatal("finite fallback did not reach provider")
	}
	legacy, legacyPrincipal, legacyModel, legacyService, actor := aiQuotaLegacyFixture(t)
	if response := aiQuotaPut(t, legacy, legacyPrincipal.TenantID, actor, aiQuotaCaps(10)); response.Code != 204 {
		t.Fatal(response.Code, response.Body)
	}
	if _, err = legacy.Exec(t.Context(), "UPDATE ai_provider_configs SET mode='platform' WHERE tenant_id=$1", legacyPrincipal.TenantID); err != nil {
		t.Fatal(err)
	}
	approveMailboxAICloud(t, legacy, legacyPrincipal.TenantID, true)
	if _, err = legacyService.Run(t.Context(), legacyPrincipal, "platform-cannot-use-host-caps", input, mailboxAIVersion(t, legacy, legacyPrincipal.TenantID)); !errors.Is(err, errAIBudget) || legacyModel.calls.Load() != 0 {
		t.Fatal("host caps bypassed platform plan and term", err)
	}
}
func mustAIQuotaConfig(t *testing.T, pool *db.Pool, tenant int64) store.AiProviderConfigs {
	t.Helper()
	cfg, err := store.New(pool).PlatformGetAIProvider(t.Context(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
func TestAIQuotaCalendarMonthDSTAndFrozenProviderBudgetPG(t *testing.T) {
	pool, principal, _, _, _, _ := providerBudgetFixtureUnseeded(t, "byo", "classify")
	actor := providerBudgetAdmin(t, pool, principal.TenantID)
	if _, err := pool.Exec(t.Context(), `INSERT INTO settings(scope,scope_id,key,value)VALUES('tenant',$1,'business_timezone','"America/New_York"') ON CONFLICT(scope,scope_id,key) DO UPDATE SET value=EXCLUDED.value`, principal.TenantID); err != nil {
		t.Fatal(err)
	}
	body := aiQuotaCaps(3)
	body["daily_token_limit"] = 1
	body["monthly_token_limit"] = 100
	if response := aiQuotaPut(t, pool, principal.TenantID, actor, body); response.Code != 204 {
		t.Fatal(response.Code, response.Body)
	}
	cfg := mustAIQuotaConfig(t, pool, principal.TenantID)
	if cfg.QuotaTimezone != "America/New_York" {
		t.Fatal(cfg.QuotaTimezone)
	}
	location, err := time.LoadLocation(cfg.QuotaTimezone)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.March, 9, 0, 30, 0, 0, location)
	start, end, err := aiQuotaMonth(now, cfg.QuotaTimezone)
	if err != nil || end.Sub(start) != 31*24*time.Hour-time.Hour {
		t.Fatal("DST month used fixed UTC duration", start, end, err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,provider,model,cost_currency,input_rate,output_rate,status,reserved_tokens,reserved_cost_micros,created_at) VALUES($1,$2,'dst-previous-local-day-row',repeat('b',64),$3,$4,'byo','openai','old-byo','CNY',0,0,'uncertain',100,0,$5)`, principal.TenantID, principal.MailboxID, start, end, time.Date(2026, time.March, 8, 23, 30, 0, 0, location)); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.PlatformTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(t.Context(), principal.TenantID); err != nil {
		t.Fatal(err)
	}
	if err = checkAIProviderBudget(t.Context(), q, cfg, principal.TenantID, 1, 0, now); !errors.Is(err, ErrAIBudgetExceeded) {
		t.Fatal("monthly usage lost across DST daily boundary", err)
	}
	cfg.MonthlyTokenLimit = 101
	if err = checkAIProviderBudget(t.Context(), q, cfg, principal.TenantID, 1, 0, now); err != nil {
		t.Fatal("previous local day consumed new daily budget", err)
	}
}

func TestAINonplatformQuotaMailboxAndConcurrentLimitsPG(t *testing.T) {
	t.Run("mailbox_and_tenant", func(t *testing.T) {
		pool, principal, model, service, actor := aiQuotaLegacyFixture(t)
		caps := aiQuotaCaps(4)
		caps["nonplatform_mailbox_call_limit"] = 2
		if response := aiQuotaPut(t, pool, principal.TenantID, actor, caps); response.Code != 204 {
			t.Fatal(response.Code, response.Body)
		}
		var otherID int64
		if err := pool.QueryRow(t.Context(), `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash)SELECT tenant_id,domain_id,'another','another@ai.example.test','test' FROM mailboxes WHERE tenant_id=$1 AND id=$2 RETURNING id`, principal.TenantID, principal.MailboxID).Scan(&otherID); err != nil {
			t.Fatal(err)
		}
		other := principal
		other.MailboxID = otherID
		other.Address = "another@ai.example.test"
		other.SessionHash = strings.Repeat("d", 64)
		if _, err := store.New(pool).PlatformCreateMailboxSession(t.Context(), store.PlatformCreateMailboxSessionParams{TokenHash: other.SessionHash, Address: other.Address}); err != nil {
			t.Fatal(err)
		}
		target := mailboxAIVersion(t, pool, principal.TenantID)
		input := ai.OperationInput{Operation: "classify", Body: "Synthetic same-tenant mailbox fixture"}
		for _, p := range []store.PlatformConsumeMailboxTicketRow{principal, other} {
			for n := range 2 {
				if _, err := service.Run(t.Context(), p, fmt.Sprintf("per-mailbox-%d-%d-quota", p.MailboxID, n), input, target); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := service.Run(t.Context(), p, fmt.Sprintf("per-mailbox-%d-over-quota", p.MailboxID), input, target); !errors.Is(err, errAIBudget) {
				t.Fatal("mailbox/tenant cap bypassed", err)
			}
		}
		if usage := aiQuotaUsage(t, pool, principal); usage.CallsUsed != 4 || usage.MailboxCallsUsed != 2 || model.calls.Load() != 4 {
			t.Fatal("mailbox usage was conflated with tenant", usage, model.calls.Load())
		}
	})
	t.Run("concurrent_reserved", func(t *testing.T) {
		pool, principal, model, service, actor := aiQuotaLegacyFixture(t)
		caps := aiQuotaCaps(20)
		caps["nonplatform_concurrency_limit"] = 1
		if response := aiQuotaPut(t, pool, principal.TenantID, actor, caps); response.Code != 204 {
			t.Fatal(response.Code, response.Body)
		}
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		defer unblock()
		service.Provider = func(store.AiProviderConfigs) (ai.Provider, error) {
			return &quotaBlockingModel{providerBudgetModel: model, entered: entered, release: release}, nil
		}
		target := mailboxAIVersion(t, pool, principal.TenantID)
		input := ai.OperationInput{Operation: "classify", Body: "Synthetic concurrency fixture"}
		first := make(chan error, 1)
		go func() {
			_, e := service.Run(t.Context(), principal, "explicit-host-concurrency-first", input, target)
			first <- e
		}()
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("first reservation did not reach provider")
		}
		var group sync.WaitGroup
		for index := range 7 {
			group.Add(1)
			go func(n int) {
				defer group.Done()
				_, _ = service.Run(t.Context(), principal, fmt.Sprintf("explicit-host-concurrency-%02d", n), input, target)
			}(index)
		}
		group.Wait()
		unblock()
		if err := <-first; err != nil {
			t.Fatal(err)
		}

		if model.calls.Load() != 1 {
			t.Fatal("concurrent durable reservation exceeded host cap", model.calls.Load())
		}
	})
}

type quotaBlockingModel struct {
	*providerBudgetModel
	entered chan<- struct{}
	release <-chan struct{}
}

func (m *quotaBlockingModel) Complete(ctx context.Context, request ai.CompletionRequest) (ai.CompletionResponse, error) {
	select {
	case m.entered <- struct{}{}:
	default:
	}
	select {
	case <-m.release:
		return m.providerBudgetModel.Complete(ctx, request)
	case <-ctx.Done():
		return ai.CompletionResponse{}, ctx.Err()
	}
}
