package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestMarketingGatewayOnlyConfigurationAndLegacyIPDisable(t *testing.T) {
	p, _ := authIntegrationDatabase(t)
	ctx := t.Context()
	q := store.New(p.Pool)
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := p.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := p.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('gateway-scope','Gateway') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug) VALUES($1,'Gateway','gateway-scope') RETURNING id", brand)
	other := scalar("INSERT INTO tenants(brand_id,name,slug) VALUES($1,'Other','gateway-other') RETURNING id", brand)
	staff := scalar("INSERT INTO users(email,password_hash) VALUES('gateway-staff@example.test','unused') RETURNING id")
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", staff)
	box, err := secrets.New([]byte(strings.Repeat("g", 32)))
	if err != nil {
		t.Fatal(err)
	}
	h := &MarketingHandler{Store: q, Platform: q, Secrets: box}
	r := gin.New()
	r.Use(middleware.AuditPlatform(q, zap.NewNop()), func(c *gin.Context) {
		c.Set(middleware.ContextUserID, staff)
		c.Set(middleware.ContextTenantID, tenant)
	}, middleware.RequirePlatformAdmin(q))
	tx := middleware.RequirePlatformTx(p, zap.NewNop())
	r.POST("/api/v1/platform/tenants/:id/marketing/channel", tx, h.ConfigureChannel)
	r.PATCH("/api/v1/platform/tenants/:id/marketing/channels/:channel_id", tx, h.ConfigureChannel)
	r.GET("/api/v1/marketing/channels", h.Channels)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	createPath := fmt.Sprintf("/api/v1/platform/tenants/%d/marketing/channel", tenant)
	for _, body := range []string{`{"name":"Rejected IP","kind":"dedicated_ip","enabled":true}`, `{"name":"Rejected IP","kind":"dedicated_ip","enabled":false}`, `{"name":"Rejected IP","kind":"dedicated_ip"}`} {
		w := call(http.MethodPost, createPath, body)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "MARKETING_CHANNEL_UNSUPPORTED") {
			t.Fatalf("new IP entry%d %s", w.Code, w.Body)
		}
	}
	if scalar("SELECT count(*) FROM marketing_channels WHERE tenant_id=$1", tenant) != 0 {
		t.Fatal("disabled IP configuration was still created")
	}
	gatewayBody := `{"name":"Gateway","kind":"dedicated_gateway","enabled":true,"smtp":{"host":"relay.example.test","port":465,"from":"sender@example.test","dedicated":true,"implicit_tls":true}}`
	created := call(http.MethodPost, createPath, gatewayBody)
	if created.Code != 201 {
		t.Fatalf("gateway create%d %s", created.Code, created.Body)
	}
	var gateway struct {
		Channel struct {
			ID int64 `json:"id"`
		} `json:"channel"`
	}
	if err = json.Unmarshal(created.Body.Bytes(), &gateway); err != nil {
		t.Fatal(err)
	}
	gatewayPath := fmt.Sprintf("/api/v1/platform/tenants/%d/marketing/channels/%d", tenant, gateway.Channel.ID)
	if w := call(http.MethodPatch, gatewayPath, `{"name":"Gateway","kind":"dedicated_ip","enabled":false}`); w.Code != 409 {
		t.Fatalf("gateway converted to IP%d", w.Code)
	}
	seedIP := func(owner int64, name string) int64 {
		return scalar("INSERT INTO marketing_channels(tenant_id,name,kind,sender_address,encrypted_config,enabled) VALUES($1,$2,'dedicated_ip','legacy@example.test','enc:v1:retained-legacy-fixture',true) RETURNING id", owner, name)
	}
	legacy := seedIP(tenant, "Legacy IP")
	foreign := seedIP(other, "Other IP")
	rollback := seedIP(tenant, "Rollback IP")
	path := fmt.Sprintf("/api/v1/platform/tenants/%d/marketing/channels/%d", tenant, legacy)
	for _, body := range []string{`{}`, `{"enabled":true}`, `{"name":"Renamed IP","kind":"dedicated_ip","enabled":false}`, `{"enabled":false,"smtp":{"host":"replacement.example.test"}}`, strings.Replace(gatewayBody, `"name":"Gateway"`, `"name":"Legacy IP"`, 1)} {
		w := call(http.MethodPatch, path, body)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "MARKETING_CHANNEL_UNSUPPORTED") {
			t.Fatalf("legacy IP mutation%d %s", w.Code, w.Body)
		}
	}
	if scalar("SELECT count(*) FROM marketing_channels WHERE id=$1 AND kind='dedicated_ip' AND enabled", legacy) != 1 {
		t.Fatal("unsupported mutation changed legacy transport")
	}
	foreignPath := fmt.Sprintf("/api/v1/platform/tenants/%d/marketing/channels/%d", tenant, foreign)
	if w := call(http.MethodPatch, foreignPath, `{"enabled":false}`); w.Code != 404 {
		t.Fatalf("cross-tenant close%d %s", w.Code, w.Body)
	}
	if scalar("SELECT count(*) FROM marketing_channels WHERE id=$1 AND enabled", foreign) != 1 {
		t.Fatal("cross-tenant legacy channel was closed")
	}
	h.Secrets = nil // Disabling old transport must not require decrypting/rewriting it.
	disabled := call(http.MethodPatch, path, `{"enabled":false}`)
	if disabled.Code != 200 || !strings.Contains(disabled.Body.String(), `"capability_available":false`) || strings.Contains(disabled.Body.String(), "enc:v1:") {
		t.Fatalf("legacy disable%d %s", disabled.Code, disabled.Body)
	}
	if scalar("SELECT count(*) FROM marketing_channels WHERE id=$1 AND name='Legacy IP' AND kind='dedicated_ip' AND NOT enabled AND sender_address='legacy@example.test' AND encrypted_config='enc:v1:retained-legacy-fixture'", legacy) != 1 {
		t.Fatal("disable rewrote legacy channel metadata")
	}
	if scalar("SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND actor_user_id=$2 AND detail->>'operation'='disable_unsupported_legacy_channel' AND detail->>'status'='200'", tenant, staff) != 1 {
		t.Fatal("legacy disable lost mandatory audit")
	}
	listed := call(http.MethodGet, "/api/v1/marketing/channels", "")
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), "Legacy IP") || strings.Contains(listed.Body.String(), "enc:v1:") {
		t.Fatalf("legacy safe read%d %s", listed.Code, listed.Body)
	}
	if w := call(http.MethodPost, createPath, `{"name":"No secret IP","kind":"dedicated_ip","enabled":false}`); w.Code != 409 {
		t.Fatalf("missing secret allowed IP configuration%d", w.Code)
	}
	exec("CREATE FUNCTION reject_legacy_channel_close_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.detail->>'operation'='disable_unsupported_legacy_channel' AND NEW.detail->>'outcome'='succeeded' THEN RAISE EXCEPTION 'injected mandatory close audit failure'; END IF; RETURN NEW; END $$")
	exec("CREATE TRIGGER reject_legacy_channel_close_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_legacy_channel_close_audit()")
	failed := call(http.MethodPatch, fmt.Sprintf("/api/v1/platform/tenants/%d/marketing/channels/%d", tenant, rollback), `{"enabled":false}`)
	if failed.Code != 500 || !strings.Contains(failed.Body.String(), "AUDIT_UNAVAILABLE") || scalar("SELECT count(*) FROM marketing_channels WHERE id=$1 AND enabled", rollback) != 1 {
		t.Fatalf("unaudited disable committed%d %s", failed.Code, failed.Body)
	}
	if scalar("SELECT count(*) FROM marketing_campaigns WHERE tenant_id=$1", tenant) != 0 {
		t.Fatal("configuration unexpectedly created sending work")
	}
}
