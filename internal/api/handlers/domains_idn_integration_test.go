package handlers

// MAIL-004 PostgreSQL integration: the handler must hand the database the
// lowercase A-label so the existing domains.fqdn UNIQUE key is the single
// authority for "one domain, one resource".

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

type idnDBHarness struct {
	t        *testing.T
	platform *db.Pool
	tenant   int64
	user     int64
	router   *gin.Engine
}

// newIDNDBHarness seeds one tenant with an active subscription and runs the
// real handlers inside the same RLS tenant transaction production uses.
func newIDNDBHarness(t *testing.T) *idnDBHarness {
	t.Helper()
	platform, app := authIntegrationDatabase(t)
	h := &idnDBHarness{t: t, platform: platform}

	brand := h.scalar("INSERT INTO brands(slug,name) VALUES('idn','IDN') RETURNING id")
	h.tenant = h.scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'IDN','idn','active') RETURNING id", brand)
	h.user = h.scalar("INSERT INTO users(email,password_hash) VALUES('idn@example.test','unused') RETURNING id")
	h.exec("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", h.user, h.tenant)
	h.exec(`INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features)
		VALUES('idn','{"en":"IDN"}',10000,'CNY',1073741824,5,20,100,'{}')`)
	// Migration 0015 auto-provisions one active 'free' subscription per new
	// tenant; the one-active-per-tenant index forbids a second INSERT, so the
	// seeded plan is applied by updating that row.
	h.exec(`UPDATE subscriptions SET plan_id='idn', billing_cycle='monthly',
		plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='idn')
		WHERE tenant_id=$1`, h.tenant)

	domain := NewDomainHandler(store.New(app.Pool), nil)
	alias := NewAliasHandler(store.New(app.Pool), nil)
	router := gin.New()
	group := router.Group("/api/v1")
	group.Use(func(c *gin.Context) {
		c.Set(middleware.ContextTenantID, h.tenant)
		c.Set(middleware.ContextUserID, h.user)
		c.Next()
	}, middleware.RequireTenantTx(app, zap.NewNop()))
	group.POST("/domains", domain.Create)
	group.POST("/aliases", alias.Create)
	h.router = router
	return h
}

func (h *idnDBHarness) exec(sql string, args ...any) {
	h.t.Helper()
	if _, err := h.platform.Exec(context.Background(), sql, args...); err != nil {
		h.t.Fatalf("%s: %v", sql, err)
	}
}

func (h *idnDBHarness) scalar(sql string, args ...any) int64 {
	h.t.Helper()
	var n int64
	if err := h.platform.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		h.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (h *idnDBHarness) text(sql string, args ...any) string {
	h.t.Helper()
	var s string
	if err := h.platform.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		h.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (h *idnDBHarness) post(path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.router.ServeHTTP(rec, req)
	return rec
}

func TestDomainCreateStoresIDNAsALabelInPostgres(t *testing.T) {
	h := newIDNDBHarness(t)

	res := h.post("/api/v1/domains", `{"fqdn":"`+chineseSLDULabel+`"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("Unicode domain rejected: %d %s", res.Code, res.Body.String())
	}
	if got := h.text("SELECT fqdn FROM domains WHERE tenant_id=$1", h.tenant); got != chineseSLDALabel {
		t.Fatalf("stored fqdn = %q; want the A-label %q", got, chineseSLDALabel)
	}

	// Two spellings of one domain must not become two rows: the surviving row
	// is the A-label one and every other spelling collides with it.
	for _, spelling := range []string{"XN--FSQU00A.EXAMPLE", chineseSLDULabel + ".", "例子。example"} {
		body := `{"fqdn":"` + spelling + `"}`
		if res := h.post("/api/v1/domains", body); res.Code != http.StatusConflict {
			t.Fatalf("spelling %q status = %d body %s; want 409", spelling, res.Code, res.Body.String())
		}
	}
	if n := h.scalar("SELECT count(*) FROM domains WHERE tenant_id=$1", h.tenant); n != 1 {
		t.Fatalf("tenant has %d domains for one name", n)
	}

	// Existing ASCII behavior is unchanged.
	if res := h.post("/api/v1/domains", `{"fqdn":"Ascii.Example"}`); res.Code != http.StatusCreated {
		t.Fatalf("ASCII domain rejected: %d %s", res.Code, res.Body.String())
	}
	if got := h.text("SELECT fqdn FROM domains WHERE tenant_id=$1 AND fqdn LIKE 'ascii%'", h.tenant); got != "ascii.example" {
		t.Fatalf("ASCII domain stored as %q; want ascii.example", got)
	}
}

// TestDomainCreateIDNTopLevelDomainStorage records the one remaining schema
// dependency. The handler normalizes a U-label TLD correctly, but
// migrations/0023 constrains domains.fqdn's last label to [a-z]{2,63}, which
// cannot hold an A-label such as xn--fiqs8s. The probe makes the gap explicit
// today and turns into the full create assertion once the constraint allows
// A-label TLDs, so the gap cannot silently disappear.
func TestDomainCreateIDNTopLevelDomainStorage(t *testing.T) {
	h := newIDNDBHarness(t)
	if !h.idnTLDStorageAvailable() {
		t.Skip("domains_storage_fqdn_check (migrations/0023) rejects A-label TLDs; " +
			"MAIL-004 handler normalization is covered by TestDomainCreateStoresIDNAsALabelInPostgres. " +
			"A follow-up migration must relax the fqdn check to accept A-label labels before IDN TLDs can be stored.")
	}

	res := h.post("/api/v1/domains", `{"fqdn":"`+chineseDomainULabel+`"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("Unicode TLD rejected: %d %s", res.Code, res.Body.String())
	}
	if got := h.text("SELECT fqdn FROM domains WHERE tenant_id=$1", h.tenant); got != chineseDomainALabel {
		t.Fatalf("stored fqdn = %q; want %q", got, chineseDomainALabel)
	}
	if res := h.post("/api/v1/domains", `{"fqdn":"`+chineseDomainALabel+`"}`); res.Code != http.StatusConflict {
		t.Fatalf("second spelling of an IDN TLD domain created a row: %d %s", res.Code, res.Body.String())
	}
}

// idnTLDStorageAvailable probes the constraint with the exact value the
// handler produces, so the skip condition is the real database behavior
// rather than a copy of the migration's regular expression.
func (h *idnDBHarness) idnTLDStorageAvailable() bool {
	h.t.Helper()
	ctx := context.Background()
	_, err := h.platform.Exec(ctx,
		"INSERT INTO domains(tenant_id,fqdn,status,verification_token) VALUES($1,$2,'pending_dns','idn-probe')",
		h.tenant, chineseDomainALabel)
	if err == nil {
		// Leave no fixture behind: the create test owns the tenant's rows.
		if _, delErr := h.platform.Exec(ctx, "DELETE FROM domains WHERE fqdn=$1", chineseDomainALabel); delErr != nil {
			h.t.Fatalf("cleanup probe row: %v", delErr)
		}
		return true
	}
	if strings.Contains(err.Error(), "domains_storage_fqdn_check") {
		return false
	}
	h.t.Fatalf("unexpected IDN TLD probe failure: %v", err)
	return false
}
