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
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// TestAIQuotaBrowserFixture serves actual panel auth, CSRF and quota endpoints
// against an owned database. No model provider or production is contacted.
func TestAIQuotaBrowserFixture(t *testing.T) {
	directory := os.Getenv("URMAIL_AI_QUOTA_BROWSER_DIR")
	if directory == "" {
		t.Skip("private quota browser fixture directory required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	pool, app := testdb.OpenMigrated(t)
	ctx := t.Context()
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx, query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	execute := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('default','AI quota browser') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'AI quota browser','ai-quota-browser','active') RETURNING id", brand)
	password := "Isolated-quota-browser-only!42"
	hash, err := panelpassword.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	admin := scalar("INSERT INTO users(email,password_hash) VALUES('quota-platform@example.test',$1) RETURNING id", hash)
	owner := scalar("INSERT INTO users(email,password_hash) VALUES('quota-owner@example.test',$1) RETURNING id", hash)
	execute("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$3,'owner'),($2,$3,'admin')", owner, admin, tenant)
	execute("INSERT INTO platform_admins(user_id) VALUES($1)", admin)
	mailboxHash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'quota.example.test','active','owned-fixture',now()) RETURNING id", tenant)
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash) VALUES($1,$2,'probe','probe@quota.example.test',$3) RETURNING id", tenant, domain, mailboxHash)

	execute("INSERT INTO ai_provider_configs(tenant_id,mode,provider,endpoint,model,enabled,local_only) VALUES($1,'local','ollama','http://127.0.0.1:11434','isolated-fixture',false,true)", tenant)
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.New(api.Options{PlatformPool: pool, AppPool: app, Logger: zap.NewNop(), Messages: messages, WorkersHeld: true, WebmailNative: true, AILocalEndpoints: []string{"http://127.0.0.1:11434"}}))
	defer server.Close()
	raw, err := json.Marshal(map[string]any{"url": server.URL, "platform_email": "quota-platform@example.test", "owner_email": "quota-owner@example.test", "password": password, "tenant_id": tenant, "mailbox_id": mailbox, "mailbox_address": "probe@quota.example.test", "scope": "Owned actual PostgreSQL/auth/CSRF/router; synthetic tenant; no provider calls"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("Private AI quota browser fixture ready")
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		if done, readErr := os.ReadFile(filepath.Join(directory, "browser.done")); readErr == nil {
			if strings.TrimSpace(string(done)) != "PASS" {
				t.Fatal("browser reported failure")
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	t.Fatal("AI quota browser timed out")
}
