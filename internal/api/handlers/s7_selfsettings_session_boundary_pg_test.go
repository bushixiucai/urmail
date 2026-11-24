package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// TestSelfSettingsStayBoundToTheSessionTenant exercises the real session
// lookup and self-settings adapter on PostgreSQL: each session only reads its
// own tenant's settings, the (tenant_id, mailbox_id) pair cannot be rewritten
// into a cross-tenant session, and a suspended tenant makes the same token
// unauthorized instead of widening the adapter.
func TestSelfSettingsStayBoundToTheSessionTenant(t *testing.T) {
	p, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	password := "isolated-fixture-password"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	type account struct {
		tenant, mailbox int64
		address, name   string
	}
	accounts := make([]account, 0, 2)
	for i, name := range []string{"tenant-a", "tenant-b"} {
		var tenant, domain, mailbox int64
		if err := p.QueryRow(ctx, `WITH b AS(INSERT INTO brands(slug,name) VALUES($1,$1) RETURNING id),t AS(INSERT INTO tenants(brand_id,name,slug,status) SELECT id,$1,$1,'active' FROM b RETURNING id) SELECT id FROM t`, name).Scan(&tenant); err != nil {
			t.Fatal(err)
		}
		if err := p.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,$2,'active','fixture',now()) RETURNING id`, tenant, name+".example.test").Scan(&domain); err != nil {
			t.Fatal(err)
		}
		address := []string{"alice@tenant-a.example.test", "bob@tenant-b.example.test"}[i]
		if err := p.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,$3,$4,$5,1048576) RETURNING id`, tenant, domain, []string{"alice", "bob"}[i], address, hash).Scan(&mailbox); err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, account{tenant: tenant, mailbox: mailbox, address: address, name: name})
	}
	self := StoreMailboxSelfSettings{Pool: p}
	for i, acct := range accounts {
		if _, err := self.SaveSettings(ctx, MailboxSettingsSave{TenantID: acct.tenant, MailboxID: acct.mailbox, DisplayName: acct.name + " display", Preferences: json.RawMessage("{}"), IdentityProfiles: json.RawMessage("{}")}); err != nil {
			t.Fatalf("seed settings %d: %v", i, err)
		}
	}

	auth := StoreMailboxNative{Q: db.Queries(p)}
	native := &MailboxNativeHandler{Enabled: true, Auth: auth, Sessions: auth, Vault: webmail.NewMemoryVault(), Pool: p, SelfSettings: self}
	router := gin.New()
	router.POST("/api/v1/mailbox/session", native.CreateSession)
	group := router.Group("/api/v1/mailbox", middleware.RequireMailboxAuth(auth, native.Vault))
	group.GET("/settings", native.GetSettings)
	server := httptest.NewServer(router)
	defer server.Close()

	login := func(address string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"email": address, "password": password})
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/mailbox/session", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out struct {
			Token string `json:"token"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Token == "" {
			t.Fatalf("login %s status=%d", address, resp.StatusCode)
		}
		return out.Token
	}
	settings := func(token string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/mailbox/settings", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	tokens := []string{login(accounts[0].address), login(accounts[1].address)}
	for i, token := range tokens {
		status, body := settings(token)
		if status != http.StatusOK {
			t.Fatalf("account %d settings status=%d body=%v", i, status, body)
		}
		if body["display_name"] != accounts[i].name+" display" {
			t.Fatalf("account %d read foreign settings: %v", i, body["display_name"])
		}
		if body["address"] != accounts[i].address {
			t.Fatalf("account %d address=%v want %s", i, body["address"], accounts[i].address)
		}
	}

	// The session row's tenant/mailbox pair is enforced by the database: a
	// rewrite into another tenant is refused before any lookup.
	if _, err := p.Exec(ctx, `UPDATE mailbox_sessions SET tenant_id=$1 WHERE tenant_id=$2 AND mailbox_id=$3`, accounts[1].tenant, accounts[0].tenant, accounts[0].mailbox); err == nil {
		t.Fatal("cross-tenant session rewrite accepted")
	}
	// A suspended tenant invalidates the still-unexpired token: the session
	// query joins tenant status, so no settings are served.
	if _, err := p.Exec(ctx, `UPDATE tenants SET status='suspended' WHERE id=$1`, accounts[0].tenant); err != nil {
		t.Fatal(err)
	}
	if status, body := settings(tokens[0]); status != http.StatusUnauthorized {
		t.Fatalf("suspended tenant session status=%d body=%v", status, body)
	}
	if _, err := p.Exec(ctx, `UPDATE tenants SET status='active' WHERE id=$1`, accounts[0].tenant); err != nil {
		t.Fatal(err)
	}
	// Suspension revokes the live session durably; after the tenant returns a
	// fresh login reads only its own settings again.
	if status, body := settings(tokens[0]); status != http.StatusUnauthorized {
		t.Fatalf("suspended-session revocation was not durable: %d %v", status, body)
	}
	fresh := login(accounts[0].address)
	if status, body := settings(fresh); status != http.StatusOK || body["display_name"] != accounts[0].name+" display" {
		t.Fatalf("restored tenant settings status=%d body=%v", status, body)
	}
}
