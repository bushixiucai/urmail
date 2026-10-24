package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestAIUsageOrphanReservationCanBeReviewedWithoutAnotherMailboxCall(t *testing.T) {
	p, principal := mailboxAIFixture(t, 10)
	ctx := t.Context()
	var id, owner int64
	if err := p.QueryRow(ctx, "SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner'", principal.TenantID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO platform_admins(user_id) VALUES($1) ON CONFLICT DO NOTHING", owner); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,reserved_tokens,reserved_cost_micros,provider,model,cost_currency,input_rate,output_rate,updated_at) VALUES($1,$2,'orphan-reserved-request',repeat('a',64),now(),now()+interval '1 month','platform',100,100,'openai','fixture','CNY',1000000,1000000,now()-interval '10 minutes') RETURNING id", principal.TenantID, principal.MailboxID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	h := &AIConfigurationHandler{Pool: p}
	router := gin.New()
	router.POST("/:id/:request_id", func(c *gin.Context) { c.Set(middleware.ContextUserID, owner); c.Next() }, h.ResolveUsage)
	response := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/"+strconv.FormatInt(principal.TenantID, 10)+"/"+strconv.FormatInt(id, 10), strings.NewReader(`{"status":"resolved","tokens":15,"cost_micros":20,"cost_currency":"CNY","reason":"Verified upstream accounting after the worker crash"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("orphan reserve could not converge: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"result"`) || strings.Contains(response.Body.String(), `"request_key"`) || strings.Contains(response.Body.String(), `"input_digest"`) {
		t.Fatal("platform accounting response leaked mailbox completion/cache identity")
	}
	var status string
	var tokens, cost int64
	if err := p.QueryRow(ctx, "SELECT status,actual_tokens,actual_cost_micros FROM ai_usage_requests WHERE id=$1", id).Scan(&status, &tokens, &cost); err != nil || status != "resolved" || tokens != 15 || cost != 20 {
		t.Fatalf("reconciliation did not preserve observed accounting: %s %d %d %v", status, tokens, cost, err)
	}
	if pending, err := store.New(p).PlatformPendingAIUsage(ctx, principal.TenantID); err != nil || len(pending) != 0 {
		t.Fatal("resolved orphan remains in unfinished accounting list", err)
	}
}

func TestAIUsageMetadataDoesNotRoundLargeObservedTokens(t *testing.T) {
	p, principal := mailboxAIFixture(t, 10)
	ctx := t.Context()
	if _, err := p.Exec(ctx, "INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,status,reserved_tokens,reserved_cost_micros,actual_tokens,provider,model,cost_currency,input_rate,output_rate) VALUES($1,$2,'huge-observed-token-count',repeat('b',64),now(),now()+interval '1 month','platform','uncertain',100,100,9007199254740993,'openai','fixture','CNY',1000000,1000000)", principal.TenantID, principal.MailboxID); err != nil {
		t.Fatal(err)
	}
	h := &AIConfigurationHandler{Pool: p}
	router := gin.New()
	router.GET("/", func(c *gin.Context) { c.Set(middleware.ContextTenantID, principal.TenantID); c.Next() }, h.Usage)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	var body struct {
		Requests []struct {
			Actual any    `json:"actual_tokens"`
			Exact  string `json:"actual_tokens_exact"`
		} `json:"requests"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Requests) != 1 || body.Requests[0].Actual != nil || body.Requests[0].Exact != "9007199254740993" {
		t.Fatalf("large token count rounded or hidden: %s", response.Body.String())
	}
}
