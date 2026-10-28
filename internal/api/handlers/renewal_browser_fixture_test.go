package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/billingrenew"
	"github.com/bushixiucai/urmail/internal/store"
)

// TestRecurringBrowserFixturePG exposes actual HTTP handlers and PostgreSQL
// role/CSRF enforcement to an isolated browser. Only the provider is a named
// sandbox stand-in. This opt-in fixture is never compiled into urmaild.
func TestRecurringBrowserFixturePG(t *testing.T) {
	dir := os.Getenv("URMAIL_RECURRING_BROWSER_FIXTURE_DIR")
	if dir == "" {
		t.Skip("browser fixture not requested")
	}
	f := newRecurringFixture(t)
	var freeTenant, member int64
	if err := f.s.p.QueryRow(t.Context(), "INSERT INTO tenants(brand_id,name,slug,status) SELECT brand_id,'Free recurring fixture','free-recurring-fixture','active' FROM tenants WHERE id=$1 RETURNING id", f.s.tid).Scan(&freeTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.p.Exec(t.Context(), "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", f.s.uid, freeTenant); err != nil {
		t.Fatal(err)
	}
	if err := f.s.p.QueryRow(t.Context(), "INSERT INTO users(email,password_hash) VALUES('recurring-member@example.test','unused') RETURNING id").Scan(&member); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.p.Exec(t.Context(), "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'member')", member, f.s.tid); err != nil {
		t.Fatal(err)
	}
	engine := func(tenant, user int64, svc billingrenew.EnrollmentService) *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(middleware.ContextTenantID, tenant)
			c.Set(middleware.ContextUserID, user)
			c.Set(middleware.ContextSession, store.Sessions{CsrfToken: f.csrf})
		}, middleware.RequireTenantTx(f.s.a, zap.NewNop()), middleware.RequireCSRF())
		RegisterRenewalRoutes(r.Group("/api/v1"), svc)
		return r
	}
	unsupported := *f.d
	unsupported.Adapters = nil
	engines := map[string]*gin.Engine{"supported": f.router, "unsupported": engine(f.s.tid, f.s.uid, billingrenew.EnrollmentService{Dispatcher: &unsupported, QuoteKey: f.service.QuoteKey}), "free": engine(freeTenant, f.s.uid, f.service), "readonly": engine(f.s.tid, member, f.service)}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	mode := "unsupported"
	var mu sync.Mutex
	outer := gin.New()
	outer.Use(func(c *gin.Context) {
		if c.GetHeader("X-Recurring-Fixture-Token") != token {
			c.AbortWithStatus(403)
			return
		}
		c.Next()
	})
	outer.POST("/__fixture/mode/:mode", func(c *gin.Context) {
		next := c.Param("mode")
		if engines[next] == nil {
			c.AbortWithStatus(400)
			return
		}
		mu.Lock()
		mode = next
		mu.Unlock()
		c.JSON(200, gin.H{"mode": next})
	})
	outer.POST("/__fixture/inflight", func(c *gin.Context) {
		if err := f.worker().RunOnce(c.Request.Context()); err != nil {
			c.AbortWithStatus(500)
			return
		}
		c.JSON(200, gin.H{"in_flight": true})
	})
	outer.NoRoute(func(c *gin.Context) { mu.Lock(); r := engines[mode]; mu.Unlock(); r.ServeHTTP(c.Writer, c.Request) })
	server := httptest.NewServer(outer)
	defer server.Close()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "csrf": f.csrf, "binding_id": f.binding, "provider": "explicit isolated sandbox stand-in", "modes": []string{"unsupported", "supported", "free", "readonly"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(20 * time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
			if _, e := os.Stat(filepath.Join(dir, "stop")); e == nil {
				return
			}
		}
	}
}
