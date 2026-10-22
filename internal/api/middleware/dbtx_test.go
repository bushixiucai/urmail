package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// integrationURL skips unless the database integration environment is set.
func integrationURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("URMAIL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("URMAIL_TEST_DATABASE_URL is not set; skipping the database integration tests")
	}
	return url
}

// TestRequireTenantTxScopesQueries proves the API middleware publishes the
// RLS context: queries installed into the request see only the authenticated
// tenant's rows, even though the same pool serves both tenants.
func TestRequireTenantTxScopesQueries(t *testing.T) {
	url := integrationURL(t)
	ctx := context.Background()

	migrator, err := db.NewMigrator(url)
	if err != nil {
		t.Fatalf("create migrator: %v", err)
	}
	defer func() { _ = migrator.Close() }()
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	appPool, err := db.OpenApp(ctx, url)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer appPool.Close()
	platformPool, err := db.OpenPlatform(ctx, url)
	if err != nil {
		t.Fatalf("open platform pool: %v", err)
	}
	defer platformPool.Close()

	// Seed two tenants with one domain each.
	ptx, err := platformPool.PlatformTx(ctx)
	if err != nil {
		t.Fatalf("platform tx: %v", err)
	}
	// No-op after a successful commit; ensures the pool can close on failure.
	defer func() { _ = ptx.Rollback(ctx) }()
	if _, err := ptx.Exec(ctx, "TRUNCATE domains, tenants, brands CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := ptx.Exec(ctx, "INSERT INTO brands (slug, name) VALUES ('acme-brand', 'Acme Brand')"); err != nil {
		t.Fatalf("seed brand: %v", err)
	}
	if _, err := ptx.Exec(ctx, `
		INSERT INTO tenants (brand_id, name, slug, status)
		SELECT b.id, x.name, x.slug, 'active' FROM brands b, (VALUES
			('Acme', 'acme'),
			('Beta', 'beta')
		) AS x(name, slug) WHERE b.slug = 'acme-brand'`); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	if _, err := ptx.Exec(ctx, `
		INSERT INTO domains (tenant_id, fqdn, mode, status, verification_token, tls_mode)
		SELECT t.id, d.fqdn, 'delegated', 'active', 'seed-' || d.fqdn, 'letsencrypt' FROM tenants t, (VALUES
			('acme.example', 'acme'),
			('beta.example', 'beta')
		) AS d(fqdn, slug) WHERE t.slug = d.slug`); err != nil {
		t.Fatalf("seed domains: %v", err)
	}
	if err := ptx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	var acmeID, betaID int64
	if err := platformPool.QueryRow(ctx, "SELECT id FROM tenants WHERE slug='acme'").Scan(&acmeID); err != nil {
		t.Fatalf("query acme: %v", err)
	}
	if err := platformPool.QueryRow(ctx, "SELECT id FROM tenants WHERE slug='beta'").Scan(&betaID); err != nil {
		t.Fatalf("query beta: %v", err)
	}

	// domainsQueriedBy runs a full engine request authenticated as sessionTenant
	// whose handler asks for queriedTenant's domains. The RLS context must
	// limit the result to sessionTenant's rows even when the query itself
	// names another tenant.
	domainsQueriedBy := func(t *testing.T, sessionTenant, queriedTenant int64) []string {
		t.Helper()
		gin.SetMode(gin.TestMode)

		var fqdns []string
		engine := gin.New()
		engine.Use(func(c *gin.Context) {
			c.Set(middleware.ContextTenantID, sessionTenant)
			c.Set(middleware.ContextUserID, int64(0))
			c.Next()
		})
		engine.Use(middleware.RequireTenantTx(appPool, zap.NewNop()))
		engine.GET("/api/v1/domains", func(c *gin.Context) {
			v, ok := middleware.QueriesFromCtx(c)
			if !ok {
				t.Error("queries not installed in context")
				return
			}
			q := v.(*store.Queries)
			domains, err := q.ListDomainsByTenant(c.Request.Context(), queriedTenant)
			if err != nil {
				t.Errorf("list domains: %v", err)
				return
			}
			for _, d := range domains {
				fqdns = append(fqdns, d.Fqdn)
			}
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/domains", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("request failed: status %d body %s", w.Code, w.Body.String())
		}
		return fqdns
	}

	// The handler names the other tenant's id; RLS must win.
	if got := domainsQueriedBy(t, acmeID, acmeID); len(got) != 1 || got[0] != "acme.example" {
		t.Fatalf("acme session querying its own id should see its domain, got %v", got)
	}
	if got := domainsQueriedBy(t, acmeID, betaID); len(got) != 0 {
		t.Fatalf("acme session querying beta's id must see nothing, got %v", got)
	}
	if got := domainsQueriedBy(t, betaID, betaID); len(got) != 1 || got[0] != "beta.example" {
		t.Fatalf("beta session querying its own id should see its domain, got %v", got)
	}
}
