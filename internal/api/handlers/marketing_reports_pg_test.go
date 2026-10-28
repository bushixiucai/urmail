package handlers

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestMarketingReportFactsPaginationExportAndIsolationPG(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := t.Context()
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if e := platform.QueryRow(ctx, sql, args...).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return id
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := platform.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('reports','Reports') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug) VALUES($1,'Reports','reports') RETURNING id", brand)
	other := scalar("INSERT INTO tenants(brand_id,name,slug) VALUES($1,'Other','other-reports') RETURNING id", brand)
	owner := scalar("INSERT INTO users(email,password_hash) VALUES('reports-owner@fixture.test','unused') RETURNING id")
	member := scalar("INSERT INTO users(email,password_hash) VALUES('reports-member@fixture.test','unused') RETURNING id")
	exec("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner'),($1,$3,'owner'),($4,$2,'member')", owner, tenant, other, member)
	channel := scalar("INSERT INTO marketing_channels(tenant_id,name,kind,sender_address,encrypted_config,enabled) VALUES($1,'Reports','dedicated_gateway','sender@fixture.test','enc:v1:fixture',false) RETURNING id", tenant)
	template := scalar("INSERT INTO marketing_templates(tenant_id,name,subject,text_body) VALUES($1,'Report fixture','Synthetic','Authored only') RETURNING id", tenant)
	list := scalar("INSERT INTO marketing_lists(tenant_id,name) VALUES($1,'Reports') RETURNING id", tenant)
	campaign := scalar("INSERT INTO marketing_campaigns(tenant_id,name,channel_id,template_id,list_id) VALUES($1,'=Report unsafe title',$2,$3,$4) RETURNING id", tenant, channel, template, list)
	recipient := scalar("INSERT INTO marketing_recipients(tenant_id,campaign_id,email,status) VALUES($1,$2,'=cmd@fixture.test','sent') RETURNING id", tenant, campaign)
	exec("INSERT INTO marketing_recipients(tenant_id,campaign_id,email,status) VALUES($1,$2,'unknown@fixture.test','uncertain'),($1,$2,'bounce@fixture.test','bounced'),($1,$2,'skip@fixture.test','suppressed')", tenant, campaign)
	exec("INSERT INTO marketing_recipients(tenant_id,campaign_id,email,status) SELECT $1,$2,'queued-'||i||'@fixture.test','queued' FROM generate_series(1,51) i", tenant, campaign)
	exec("INSERT INTO marketing_suppressions(tenant_id,email,reason) VALUES($1,'=cmd@fixture.test','unsubscribe')", tenant)
	for range 2 {
		exec("INSERT INTO marketing_feedback(channel_id,event_id,tenant_id,recipient_id,status,payload_hash) VALUES($1,'event-once',$2,$3,'bounced',repeat('a',64)) ON CONFLICT DO NOTHING", channel, tenant, recipient)
	}
	definition := scalar("INSERT INTO marketing_flow_definitions(tenant_id,name,draft) VALUES($1,'Report flow','{}') RETURNING id", tenant)
	runtime := scalar("INSERT INTO marketing_flows(tenant_id,name,status) VALUES($1,'Report runtime','active') RETURNING id", tenant)
	exec("INSERT INTO marketing_flow_steps(tenant_id,flow_id,step_key,kind,position,wait_seconds) VALUES($1,$2,'wait','wait',0,3600)", tenant, runtime)
	version := scalar("INSERT INTO marketing_flow_versions(tenant_id,definition_id,version,draft_revision,runtime_flow_id,definition,published_by) VALUES($1,$2,1,1,$3,'{}',$4) RETURNING id", tenant, definition, runtime, owner)
	entry := scalar("INSERT INTO marketing_flow_entries(tenant_id,definition_id,version_id,event_key,source,list_id,recipient_count) VALUES($1,$2,$3,'unique-report-event','contact_enrolled',$4,2) RETURNING id", tenant, definition, version, list)
	exec("INSERT INTO marketing_flow_entries(tenant_id,definition_id,version_id,event_key,source,list_id,recipient_count) VALUES($1,$2,$3,'unique-report-event','contact_enrolled',$4,2) ON CONFLICT DO NOTHING", tenant, definition, version, list)
	position := scalar("INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key,entry_id,stopped_at,delivery_uncertain) VALUES($1,$2,'stopped@fixture.test','wait',$3,now(),true) RETURNING id", tenant, runtime, entry)
	exec("INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key,entry_id,wait_until) VALUES($1,$2,'waiting@fixture.test','wait',$3,now()+interval '1 hour')", tenant, runtime, entry)
	for range 2 {
		exec("INSERT INTO marketing_flow_node_executions(tenant_id,position_id,lease_fence,node_key,status) VALUES($1,$2,1,'send','submitted'),($1,$2,2,'send','uncertain') ON CONFLICT DO NOTHING", tenant, position)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		actor, scope := owner, tenant
		if c.GetHeader("X-Member") == "1" {
			actor = member
		}
		if c.GetHeader("X-Other") == "1" {
			scope = other
		}
		c.Set(middleware.ContextUserID, actor)
		c.Set(middleware.ContextTenantID, scope)
	}, middleware.RequireTenantTx(app, zap.NewNop()))
	RegisterMarketingReportRoutes(router.Group("/api/v1"), store.New(app))
	call := func(method, path string, body any, header string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if header != "" {
			req.Header.Set(header, "1")
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	from, until := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Format(time.RFC3339), time.Now().UTC().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339)
	query := "?from=" + url.QueryEscape(from) + "&until=" + url.QueryEscape(until)
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("report status %d %s", w.Code, w.Body)
		}
		var out map[string]map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out["report"]
	}
	base := fmt.Sprintf("/api/v1/marketing/reports/campaigns/%d", campaign)
	report := decode(call("GET", base+query, nil, ""))
	counts := report["counts"].(map[string]any)
	if report["total_facts"] != float64(56) || len(report["rows"].([]any)) != 50 || counts["recipient/sent"] != float64(1) || counts["feedback/bounced"] != float64(1) || counts["recipient/unknown_outcome"] != float64(1) {
		t.Fatal("facts are not recomputable or event was duplicated", report)
	}
	digest := report["digest"].(string)
	next := decode(call("GET", base+query+"&offset=50&expected_digest="+digest, nil, ""))
	if len(next["rows"].([]any)) != 6 || next["digest"] != digest {
		t.Fatal("pagination omitted facts")
	}
	for _, header := range []string{"X-Other", "X-Member"} {
		w := call("GET", base+query, nil, header)
		if w.Code != 404 && w.Code != 403 {
			t.Fatal("tenant/member report exposed", w.Code)
		}
	}
	body := map[string]any{"from": from, "until": until, "expected_digest": digest, "confirmed": true}
	export := call("POST", base+"/export", body, "")
	if export.Code != 200 || export.Header().Get("X-UrMail-Report-Digest") != digest {
		t.Fatal("export rejected", export.Code, export.Body)
	}
	records, e := csv.NewReader(strings.NewReader(export.Body.String())).ReadAll()
	if e != nil || len(records) != 58 {
		t.Fatal("export silently truncated", e, len(records))
	}
	if records[1][16] != "56" || !strings.Contains(export.Body.String(), "'=cmd@fixture.test") || !strings.Contains(export.Body.String(), "'=Report unsafe title") {
		t.Fatal("metadata or formula protection missing")
	}
	if scalar("SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND actor_user_id=$2 AND action='EXPORT marketing.report' AND target_id LIKE $3", tenant, owner, "%digest="+digest+"%facts=56%") != 1 {
		t.Fatal("export audit not bound to snapshot")
	}

	// Force the request to read the old middleware role, then block on the
	// production tenant lock while another transaction revokes that actor.
	for _, change := range []string{"role", "user"} {
		lock, err := platform.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = lock.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", tenant); err != nil {
			t.Fatal(err)
		}
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- call("POST", base+"/export", body, "") }()
		deadline := time.Now().Add(3 * time.Second)
		blocked := false
		for time.Now().Before(deadline) {
			var n int
			err = platform.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())").Scan(&n)
			if err != nil {
				t.Fatal(err)
			}
			if n > 0 {
				blocked = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !blocked {
			_ = lock.Rollback(ctx)
			t.Fatal("request did not reach the post-role tenant lock")
		}
		if change == "role" {
			exec("UPDATE user_tenant_roles SET role='member' WHERE tenant_id=$1 AND user_id=$2", tenant, owner)
		} else {
			exec("UPDATE users SET status='disabled' WHERE id=$1", owner)
		}
		if err = lock.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-done:
			if result.Code != 404 && result.Code != 403 {
				t.Fatalf("revoked actor received report: %s %d", change, result.Code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("revocation request stuck")
		}
		exec("UPDATE user_tenant_roles SET role='owner' WHERE tenant_id=$1 AND user_id=$2", tenant, owner)
		exec("UPDATE users SET status='active' WHERE id=$1", owner)
	}
	exec("UPDATE marketing_recipients SET status='failed' WHERE tenant_id=$1 AND id=$2", tenant, recipient)
	if w := call("POST", base+"/export", body, ""); w.Code != 409 {
		t.Fatal("changed report exported with stale proof", w.Code)
	}
	if w := call("GET", base+query+"&offset=50&expected_digest="+digest, nil, ""); w.Code != 409 {
		t.Fatal("changed page accepted", w.Code)
	}
	if w := call("GET", base+"?from=2020-01-01T00:00:00Z&until=2026-01-01T00:00:00Z", nil, ""); w.Code != 400 {
		t.Fatal("time budget bypass")
	}
	flowBase := fmt.Sprintf("/api/v1/marketing/reports/flows/%d", definition)
	flow := decode(call("GET", flowBase+query, nil, ""))
	fc := flow["counts"].(map[string]any)
	if fc["entry/contact_enrolled"] != float64(1) || fc["entry/recipients"] != float64(2) || fc["execution/submitted"] != float64(1) || fc["instance/unknown_outcome"] != float64(1) || fc["instance/waiting"] != float64(1) {
		t.Fatal("flow aggregates lost events/unknown", fc)
	}
	fb := map[string]any{"from": from, "until": until, "expected_digest": flow["digest"], "confirmed": true}
	if w := call("POST", flowBase+"/export", fb, ""); w.Code != 200 {
		t.Fatal("flow export failed", w.Code, w.Body)
	}
	if w := call("POST", flowBase+"/export", fb, "X-Other"); w.Code != 404 {
		t.Fatal("flow cross tenant export", w.Code)
	}
	exec("INSERT INTO marketing_recipients(tenant_id,campaign_id,email) SELECT $1,$2,'budget-'||i||'@fixture.test' FROM generate_series(1,10001) i", tenant, campaign)
	if w := call("GET", base+query, nil, ""); w.Code != 413 {
		t.Fatal("row budget silently truncated", w.Code)
	}
	if w := call("POST", base+"/export", body, ""); w.Code != 413 {
		t.Fatal("oversized export emitted", w.Code)
	}
}
