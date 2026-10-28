package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/api/middleware"
)

type dnsAuthorityCapture struct {
	calls  int
	params agentd.DNSAuthorityParams
	op     string
}

func (a *dnsAuthorityCapture) Do(_ context.Context, op string, input any) (*agentd.Response, error) {
	a.calls++
	a.op = op
	raw, _ := json.Marshal(input)
	_ = json.Unmarshal(raw, &a.params)
	body := `{"domain":"authority.test","phase":"prepared","ready":false,"ds":[],"parent_ds":[]}`
	if op == agentd.OpDNSAuthorityManage {
		body = `{"changed":true,"reconcile_required":true}`
	}
	return &agentd.Response{OK: true, Stdout: body}, nil
}
func fmtAuthorityPath(id int64) string { return fmt.Sprintf("/domains/%d/authority", id) }
func TestDNSAuthorityHTTPBindsTenantDomainActorAndRejectsDelegatedPG(t *testing.T) {
	p, a := authIntegrationDatabase(t)
	ctx := t.Context()
	var brand, tenant, foreign, owner, member, domain, delegated, otherDomain int64
	insert := func(sql string, dest *int64, args ...any) {
		t.Helper()
		if err := p.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	insert("INSERT INTO brands(slug,name) VALUES('authority','Authority') RETURNING id", &brand)
	insert("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Authority','authority','active') RETURNING id", &tenant, brand)
	insert("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Other','authority-other','active') RETURNING id", &foreign, brand)
	insert("INSERT INTO users(email,password_hash) VALUES('authority-owner@example.test','unused') RETURNING id", &owner)
	insert("INSERT INTO users(email,password_hash) VALUES('authority-member@example.test','unused') RETURNING id", &member)
	if _, err := p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$3,'owner'),($2,$3,'member')", owner, member, tenant); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		tid        int64
		fqdn, mode string
		id         *int64
	}{{tenant, "authority.test", "managed", &domain}, {tenant, "delegated.test", "delegated", &delegated}, {foreign, "foreign-authority.test", "managed", &otherDomain}} {
		insert("INSERT INTO domains(tenant_id,fqdn,mode,status,verified_at,verification_token,provisioning_state) VALUES($1,$2,$3,'active',now(),'fixture','ready') RETURNING id", row.id, row.tid, row.fqdn, row.mode)
	}
	capture := &dnsAuthorityCapture{}
	handler := &DNSAuthorityHandler{App: a, Agent: capture}
	engine := func(uid int64) *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tenant); c.Set(middleware.ContextUserID, uid) }, middleware.RequireTenantTx(a, zap.NewNop()))
		RegisterDNSAuthorityRoutes(r.Group(""), handler)
		return r
	}
	call := func(uid int64, method string, id int64, body string) *httptest.ResponseRecorder {
		raw := bytes.NewBufferString(body)
		req := httptest.NewRequestWithContext(ctx, method, fmtAuthorityPath(id), raw)
		req.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		engine(uid).ServeHTTP(result, req)
		return result
	}
	if response := call(member, "POST", domain, `{"action":"prepare","confirm":true}`); response.Code != 403 {
		t.Fatalf("member %d %s", response.Code, response.Body)
	}
	if response := call(owner, "GET", otherDomain, ""); response.Code != 404 {
		t.Fatalf("foreign %d %s", response.Code, response.Body)
	}
	if response := call(owner, "GET", delegated, ""); response.Code != 409 {
		t.Fatalf("delegated %d %s", response.Code, response.Body)
	}
	for _, body := range []string{`{"action":"prepare","confirm":false}`, `{"action":"secondary-export","confirm":true}`, `{"action":"prepare","confirm":true,"path":"/etc/shadow"}`, `{"action":"prepare","confirm":true,"secondary":{"hostname":"ns2.test"}}`} {
		if response := call(owner, "POST", domain, body); response.Code != 400 {
			t.Fatalf("invalid request %d %s", response.Code, response.Body)
		}
	}
	if capture.calls != 0 {
		t.Fatal("denied caller reached root agent")
	}
	if response := call(owner, "POST", domain, `{"action":"prepare","confirm":true}`); response.Code != 200 {
		t.Fatalf("prepare %d %s", response.Code, response.Body)
	}
	if capture.params.TenantID != tenant || capture.params.DomainID != domain || capture.params.ActorID != owner || capture.params.Domain != "authority.test" || capture.op != agentd.OpDNSAuthorityManage {
		t.Fatalf("unbound agent request %+v", capture.params)
	}
	if response := call(owner, "GET", domain, ""); response.Code != 200 {
		t.Fatalf("status %d %s", response.Code, response.Body)
	}
	if capture.op != agentd.OpDNSAuthorityStatus || capture.params.Action != "" {
		t.Fatal("status called mutation operation")
	}
}
