package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
)

func TestAIConsentHTTPRequiresTenantRoleExplicitConfirmationAndCurrentTarget(t *testing.T) {
	p, principal := mailboxAIFixture(t, 2, false)
	ctx := t.Context()
	var owner int64
	if err := p.QueryRow(ctx, "SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner'", principal.TenantID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	app, err := db.OpenApp(ctx, p.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	h := &AIConfigurationHandler{Pool: p, HealthRepository: NewAIHealthRepository(p)}
	engine := func(role string) *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(middleware.ContextTenantID, principal.TenantID)
			c.Set(middleware.ContextUserID, owner)
			c.Set(middleware.ContextRole, role)
		})
		txProtected := r.Group("", middleware.RequireTenantTx(app, zap.NewNop()))
		txProtected.POST("/consent", h.Consent)
		// /ai/health probes its own short repository transactions, mirroring the
		// production route, so it must not run inside the request transaction.
		r.POST("/health", h.Health)
		return r
	}
	post := func(r *gin.Engine, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	r := engine("owner")
	digest := mailboxAIVersion(t, p, principal.TenantID)
	payload := `{"allow_cloud":true,"configuration_digest":"` + digest + `","scope_hash":"` + AICloudScopeHash + `"}`
	if w := post(r, "/consent", payload); w.Code != 400 {
		t.Fatalf("missing confirmation accepted %d", w.Code)
	}
	if _, err := p.Exec(ctx, "UPDATE user_tenant_roles SET role='member' WHERE tenant_id=$1 AND user_id=$2", principal.TenantID, owner); err != nil {
		t.Fatal(err)
	}
	if w := post(engine("member"), "/consent", `{"allow_cloud":false}`); w.Code != 403 {
		t.Fatalf("member changed consent %d", w.Code)
	}
	if _, err := p.Exec(ctx, "UPDATE user_tenant_roles SET role='owner' WHERE tenant_id=$1 AND user_id=$2", principal.TenantID, owner); err != nil {
		t.Fatal(err)
	}
	stale := strings.ReplaceAll(payload, digest, strings.Repeat("a", 64))
	stale = strings.TrimSuffix(stale, "}") + `,"confirmed":true}`
	if w := post(r, "/consent", stale); w.Code != 409 {
		t.Fatalf("stale target consent %d %s", w.Code, w.Body.String())
	}
	if w := post(r, "/health", `{}`); w.Code != 403 || !strings.Contains(w.Body.String(), "AI_CLOUD_CONSENT_REQUIRED") {
		t.Fatalf("health bypassed cloud policy %d %s", w.Code, w.Body.String())
	}
	payload = strings.TrimSuffix(payload, "}") + `,"confirmed":true}`
	if w := post(r, "/consent", payload); w.Code != 204 {
		t.Fatalf("explicit exact target denied %d %s", w.Code, w.Body.String())
	}
	if w := post(r, "/consent", `{"allow_cloud":false}`); w.Code != 204 {
		t.Fatalf("revocation denied %d", w.Code)
	}
	if w := post(r, "/health", `{}`); w.Code != 403 {
		t.Fatalf("revoked health permitted %d", w.Code)
	}
}
