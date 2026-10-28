package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// TestFinancialBrowserFixture serves production auth, CSRF and financial routes
// against an owned PostgreSQL database. No provider or production is contacted.
func TestFinancialBrowserFixture(t *testing.T) {
	dir := os.Getenv("URMAIL_FINANCIAL_BROWSER_DIR")
	if dir == "" {
		t.Skip("private financial browser fixture directory required")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	p, app := testdb.OpenMigrated(t)
	ctx := t.Context()
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if e := p.QueryRow(ctx, sql, args...).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return id
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := p.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('default','Financial browser') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Financial browser','financial-browser','active') RETURNING id", brand)
	password := "Isolated-financial-browser-only!42"
	hash, e := panelpassword.Hash(password)
	if e != nil {
		t.Fatal(e)
	}
	actor := scalar("INSERT INTO users(email,password_hash) VALUES('financial-browser@example.test',$1) RETURNING id", hash)
	exec("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", actor, tenant)
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", actor)
	order := scalar("INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency) VALUES($1,'FINANCIAL-BROWSER-REVIEW','new','pending',10000,'CNY') RETURNING id", tenant)
	payment := scalar("INSERT INTO payments(tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status,raw) VALUES($1,$2,'stripe','pi_financial_browser',10000,'CNY','succeeded','{\"client_secret\":\"NOT_FOR_EXPORT\"}') RETURNING id", tenant, order)
	review := scalar("INSERT INTO payment_manual_reviews(tenant_id,order_id,payment_id,reason) VALUES($1,$2,$3,'Synthetic verified receipt requires independent review') RETURNING id", tenant, order, payment)
	messages, e := i18n.NewStore()
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(api.New(api.Options{PlatformPool: p, AppPool: app, Logger: zap.NewNop(), Messages: messages, WorkersHeld: true}))
	defer server.Close()
	raw, _ := json.Marshal(map[string]any{"url": server.URL, "email": "financial-browser@example.test", "password": password, "tenant_id": tenant, "review_id": review, "scope": "Owned real PostgreSQL and production auth/CSRF/router; synthetic immutable cash facts; no provider calls"})
	if e = os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	t.Log("Private financial browser fixture ready")
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		if done, e := os.ReadFile(filepath.Join(dir, "browser.done")); e == nil {
			if strings.TrimSpace(string(done)) != "PASS" {
				t.Fatal("browser reported failure")
			}
			var count int
			if e = p.QueryRow(ctx, "SELECT count(*) FROM refunds WHERE tenant_id=$1 AND order_id=$2 AND status='pending'", tenant, order).Scan(&count); e != nil || count != 1 {
				t.Fatal("browser did not create exactly one pending approval", count, e)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	t.Fatal("financial browser timed out")
}
