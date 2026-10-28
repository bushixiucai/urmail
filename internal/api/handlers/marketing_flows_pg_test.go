package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	flow "github.com/bushixiucai/urmail/internal/domain/marketingflow"
	"github.com/bushixiucai/urmail/internal/marketing"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestMarketingFlowHTTPVersionEventAndRuntimeJourney(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := t.Context()
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := platform.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('flow-http','Flow') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Flow','flow-http','active') RETURNING id", brand)
	other := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Other','flow-other','active') RETURNING id", brand)
	owner := scalar("INSERT INTO users(email,password_hash) VALUES('flow-owner@example.test','unused') RETURNING id")
	member := scalar("INSERT INTO users(email,password_hash) VALUES('flow-member@example.test','unused') RETURNING id")
	exec("INSERT INTO user_tenant_roles(tenant_id,user_id,role) VALUES($1,$2,'owner'),($1,$3,'member'),($4,$2,'owner')", tenant, owner, member, other)
	template := scalar("INSERT INTO marketing_templates(tenant_id,name,subject,text_body) VALUES($1,'Welcome','Original subject','Original fixed content') RETURNING id", tenant)
	foreignTemplate := scalar("INSERT INTO marketing_templates(tenant_id,name,subject,text_body) VALUES($1,'Foreign','Private foreign subject','Private foreign body') RETURNING id", other)
	list := scalar("INSERT INTO marketing_lists(tenant_id,name) VALUES($1,'Consented') RETURNING id", tenant)
	foreignList := scalar("INSERT INTO marketing_lists(tenant_id,name) VALUES($1,'Foreign') RETURNING id", other)
	exec("INSERT INTO marketing_contacts(tenant_id,list_id,email,consent_source,opted_in) VALUES($1,$2,'reader@allowed.test','explicit-fixture',true),($1,$2,'blocked@allowed.test','explicit-fixture',true),($1,$2,'optout@allowed.test','explicit-fixture',false)", tenant, list)
	exec("INSERT INTO marketing_suppressions(tenant_id,email,reason) VALUES($1,'blocked@allowed.test','unsubscribe')", tenant)
	exec("INSERT INTO marketing_channels(tenant_id,name,kind,sender_address,encrypted_config,enabled) VALUES($1,'Gateway','dedicated_gateway','sender@example.test','enc:v1:isolated-fixture',true)", tenant)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		actor := owner
		if c.GetHeader("X-Test-Member") == "1" {
			actor = member
		}
		scope := tenant
		if c.GetHeader("X-Test-Other") == "1" {
			scope = other
		}
		c.Set(middleware.ContextUserID, actor)
		c.Set(middleware.ContextTenantID, scope)
	})
	group := r.Group("/api/v1", middleware.RequireTenantTx(app, zap.NewNop()))
	RegisterMarketingFlowRoutes(group, store.New(app.Pool), true)
	tokens, tokenErr := domainMail.NewSuppressionManager([]byte(strings.Repeat("m", 32)))
	if tokenErr != nil {
		t.Fatal(tokenErr)
	}
	r.POST("/api/v1/marketing/unsubscribe", (&MarketingSuppressionHandler{Store: store.New(app.Pool), AppPool: app, Tokens: tokens}).OneClick)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	call := func(method, path string, body any, headers map[string]string) (int, map[string]json.RawMessage) {
		t.Helper()
		var encoded []byte
		if body != nil {
			encoded, _ = json.Marshal(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		result := map[string]json.RawMessage{}
		if response.StatusCode == http.StatusNoContent && len(data) == 0 {
			return response.StatusCode, result
		}
		if json.Unmarshal(data, &result) != nil {
			t.Fatalf("invalid JSON status=%d body=%s", response.StatusCode, data)
		}
		return response.StatusCode, result
	}
	graph := flow.Definition{SchemaVersion: 1, EventSource: "contact_enrolled", Entry: "condition", Nodes: []flow.Node{{ID: "condition", Kind: "condition", Field: "email_domain", Operator: "equals", Value: "allowed.test", Next: "wait", Otherwise: "end"}, {ID: "wait", Kind: "wait", WaitSeconds: 1, Next: "send"}, {ID: "send", Kind: "send", TemplateID: template, Next: "end"}, {ID: "end", Kind: "stop"}}}
	create := map[string]any{"name": "Welcome flow", "draft": graph}
	if status, _ := call(http.MethodPost, "/api/v1/marketing/flows", create, map[string]string{"X-Test-Member": "1"}); status != 403 {
		t.Fatalf("member create=%d", status)
	}
	status, data := call(http.MethodPost, "/api/v1/marketing/flows", create, nil)
	if status != 201 {
		t.Fatalf("create=%d %s", status, data)
	}
	var definition struct {
		ID            int64 `json:"id"`
		DraftRevision int64 `json:"draft_revision"`
	}
	if err := json.Unmarshal(data["flow"], &definition); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/api/v1/marketing/flows/%d", definition.ID)
	if status, _ = call(http.MethodGet, base, nil, map[string]string{"X-Test-Other": "1"}); status != 404 {
		t.Fatalf("cross-tenant definition=%d", status)
	}
	foreign := graph
	foreign.Nodes = append([]flow.Node(nil), graph.Nodes...)
	foreign.Nodes[2].TemplateID = foreignTemplate
	if status, data = call(http.MethodPost, base+"/preview", map[string]any{"draft": foreign, "sample_email": "reader@allowed.test", "list_id": list}, nil); status != 422 || strings.Contains(string(data["errors"]), "Private foreign") {
		t.Fatalf("foreign template=%d %s", status, data)
	}
	cycle := graph
	cycle.Nodes = append([]flow.Node(nil), graph.Nodes...)
	cycle.Nodes[2].Next = "condition"
	if status, data = call(http.MethodPost, base+"/preview", map[string]any{"draft": cycle, "sample_email": "reader@allowed.test", "list_id": list}, nil); status != 422 || !strings.Contains(string(data["errors"]), "cycle") {
		t.Fatalf("cycle=%d %s", status, data)
	}
	if status, data = call(http.MethodPost, base+"/preview", map[string]any{"draft": graph, "sample_email": "reader@allowed.test", "list_id": list, "at": "2026-03-08T01:30:00-05:00"}, nil); status != 200 || !strings.Contains(string(data["path"]), "Original fixed content") || !strings.Contains(string(data["path"]), "2026-03-08T06:30:01Z") {
		t.Fatalf("preview=%d %s", status, data)
	}
	if scalar("SELECT count(*) FROM marketing_flows WHERE tenant_id=$1", tenant) != 0 || scalar("SELECT count(*) FROM marketing_flow_positions WHERE tenant_id=$1", tenant) != 0 {
		t.Fatal("preview created execution state")
	}
	if status, data = call(http.MethodPost, base+"/publish", map[string]any{"expected_revision": 1}, nil); status != 201 {
		t.Fatalf("publish=%d %s", status, data)
	}
	if status, _ = call(http.MethodPost, base+"/publish", map[string]any{"expected_revision": 1}, nil); status != 200 || scalar("SELECT count(*) FROM marketing_flow_versions WHERE tenant_id=$1", tenant) != 1 {
		t.Fatal("publish retry was not idempotent")
	}
	enroll := map[string]any{"event_key": "event-original-0001", "source": "contact_enrolled", "list_id": list}
	if status, data = call(http.MethodPost, base+"/events", enroll, nil); status != 201 || string(data["enrolled_count"]) != "1" {
		t.Fatalf("enroll=%d %s", status, data)
	}
	if status, _ = call(http.MethodPost, base+"/events", map[string]any{"event_key": "forged-event-0001", "source": "payment_received", "list_id": list}, nil); status != 400 {
		t.Fatalf("forged source=%d", status)
	}
	if status, _ = call(http.MethodPost, base+"/events", map[string]any{"event_key": "foreign-list-0001", "source": "contact_enrolled", "list_id": foreignList}, nil); status != 404 {
		t.Fatalf("foreign list=%d", status)
	}
	runtime := scalar("SELECT runtime_flow_id FROM marketing_flow_versions WHERE tenant_id=$1 AND version=1", tenant)
	exec("UPDATE marketing_templates SET subject='Edited subject',text_body='Edited content' WHERE tenant_id=$1 AND id=$2", tenant, template)
	if status, data = call(http.MethodGet, base+"/versions/1", nil, nil); status != 200 || !strings.Contains(string(data["templates"]), "Original fixed content") || strings.Contains(string(data["templates"]), "Edited content") {
		t.Fatalf("immutable content=%d %s", status, data)
	}
	if _, err := platform.Exec(ctx, "UPDATE marketing_flow_steps SET next_key='' WHERE tenant_id=$1 AND flow_id=$2 AND step_key='send'", tenant, runtime); err == nil {
		t.Fatal("published step mutated")
	}
	if _, err := platform.Exec(ctx, "UPDATE marketing_flow_content_snapshots SET subject='forged' WHERE tenant_id=$1 AND runtime_flow_id=$2", tenant, runtime); err == nil {
		t.Fatal("published content mutated")
	}
	if status, data = call(http.MethodPut, base, map[string]any{"name": "Welcome v2", "draft": graph, "expected_revision": 1}, nil); status != 200 {
		t.Fatalf("save next revision=%d %s", status, data)
	}
	if status, _ = call(http.MethodPut, base, map[string]any{"name": "stale", "draft": graph, "expected_revision": 1}, nil); status != 409 {
		t.Fatal("stale draft overwrote revision")
	}
	if status, data = call(http.MethodPost, base+"/publish", map[string]any{"expected_revision": 2}, nil); status != 201 {
		t.Fatalf("publish v2=%d %s", status, data)
	}
	if status, data = call(http.MethodPost, base+"/events", enroll, nil); status != 200 || string(data["duplicate"]) != "true" || scalar("SELECT count(*) FROM marketing_flow_positions WHERE tenant_id=$1", tenant) != 1 {
		t.Fatal("old event re-entered a new version")
	}
	var wg sync.WaitGroup
	responses := make(chan int, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := call(http.MethodPost, base+"/events", map[string]any{"event_key": "concurrent-event-0002", "source": "contact_enrolled", "list_id": list}, nil)
			responses <- code
		}()
	}
	wg.Wait()
	close(responses)
	for code := range responses {
		if code != 200 && code != 201 {
			t.Fatalf("concurrent event status=%d", code)
		}
	}
	if scalar("SELECT count(*) FROM marketing_flow_entries WHERE tenant_id=$1 AND event_key='concurrent-event-0002'", tenant) != 1 || scalar("SELECT count(*) FROM marketing_flow_positions WHERE tenant_id=$1", tenant) != 2 {
		t.Fatal("duplicate concurrent events created instances")
	}
	var subjects []string
	content := marketing.TemplateFlowContent(platform)
	sender := func(sendCtx context.Context, sendTenant, sendFlow int64, _, key string) error {
		subject, _, ok, err := content(sendCtx, sendTenant, sendFlow, key)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("content missing")
		}
		subjects = append(subjects, subject)
		return nil
	}
	worker, err := marketing.NewFlowWorker(marketing.FlowOptions{Pool: platform, Sender: sender, Enabled: true, Lease: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worker.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if scalar("SELECT count(*) FROM marketing_flow_positions WHERE tenant_id=$1 AND step_key='wait' AND wait_until>now()", tenant) != 2 {
		t.Fatal("condition did not arm version-pinned wait")
	}
	restarted, err := marketing.NewFlowWorker(marketing.FlowOptions{Pool: platform, Sender: sender, Enabled: true, Lease: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err = restarted.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 2 || subjects[0] != "Original subject" || subjects[1] != "Edited subject" {
		t.Fatalf("version content=%v", subjects)
	}
	status, data = call(http.MethodGet, base+"/instances", nil, nil)
	if status != 200 || !strings.Contains(string(data["instances"]), "completed") {
		t.Fatalf("instance visibility=%d %s", status, data)
	}
	instance := scalar("SELECT id FROM marketing_flow_positions WHERE tenant_id=$1 AND flow_id=$2", tenant, runtime)
	if status, data = call(http.MethodGet, fmt.Sprintf("%s/instances/%d/executions", base, instance), nil, nil); status != 200 || !strings.Contains(string(data["executions"]), "submitted") {
		t.Fatalf("executions=%d %s", status, data)
	}
	exec("UPDATE marketing_flow_positions SET step_key='send',delivery_uncertain=true,last_error='delivery_uncertain',lease_owner=NULL,lease_until=NULL WHERE tenant_id=$1 AND id=$2", tenant, instance)
	if _, err = restarted.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 2 {
		t.Fatal("unknown outcome auto-replayed")
	}
	retryPath := fmt.Sprintf("%s/instances/%d/retry", base, instance)
	if status, _ = call(http.MethodPost, retryPath, map[string]any{"reason": "Gateway investigation completed", "confirmed_not_accepted": false}, nil); status != 409 {
		t.Fatal("unknown retry accepted without confirmation")
	}
	if status, data = call(http.MethodPost, retryPath, map[string]any{"reason": "Gateway confirmed zero acceptance", "confirmed_not_accepted": true}, nil); status != 200 {
		t.Fatalf("confirmed retry=%d %s", status, data)
	}
	if status, _ = call(http.MethodPost, fmt.Sprintf("%s/instances/%d/stop", base, instance), map[string]any{}, nil); status != 200 {
		t.Fatal("instance stop failed")
	}
	if status, _ = call(http.MethodPost, retryPath, map[string]any{"reason": "Gateway confirmed zero acceptance", "confirmed_not_accepted": true}, nil); status != 409 {
		t.Fatal("stopped instance restarted")
	}
	exec("INSERT INTO marketing_contacts(tenant_id,list_id,email,consent_source,opted_in) VALUES($1,$2,'late@allowed.test','explicit-fixture',true)", tenant, list)
	if status, data = call(http.MethodPost, base+"/events", map[string]any{"event_key": "late-subscribe-0001", "source": "contact_enrolled", "list_id": list}, nil); status != 201 {
		t.Fatalf("late enrollment=%d %s", status, data)
	}
	token := tokens.GenerateUnsubscribeToken(tenant, "late@allowed.test")
	if status, _ = call(http.MethodPost, "/api/v1/marketing/unsubscribe?token="+url.QueryEscape(token), nil, nil); status != 204 {
		t.Fatalf("public unsubscribe=%d", status)
	}
	if scalar("SELECT count(*) FROM marketing_flow_positions WHERE tenant_id=$1 AND email='late@allowed.test' AND unsubscribed", tenant) != 1 {
		t.Fatal("unsubscribe did not stop every pending step")
	}
	if _, err = restarted.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 2 {
		t.Fatal("unsubscribed instance sent a later step")
	}
	if status, _ = call(http.MethodPost, base+"/deactivate", map[string]any{}, nil); status != 200 {
		t.Fatal("deactivation failed")
	}
	if scalar("SELECT count(*) FROM marketing_flows WHERE tenant_id=$1 AND status='active'", tenant) != 0 {
		t.Fatal("deactivation left a version active")
	}
	if status, _ = call(http.MethodPost, base+"/events", map[string]any{"event_key": "paused-entry-0001", "source": "contact_enrolled", "list_id": list}, nil); status != 409 {
		t.Fatal("paused workflow accepted new enrollment")
	}
	var visible int
	if err = app.QueryRow(ctx, "SELECT count(*) FROM marketing_flow_definitions").Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("RLS context leaked into pool: %d %v", visible, err)
	}
}
