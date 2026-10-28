package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/supportdiag"
)

func TestSupportDiagnosticsConsentPreviewBoundTicketAndIsolationPG(t *testing.T) {
	p, a := authIntegrationDatabase(t)
	ctx := context.Background()
	var brand, tenant, owner, member, domain int64
	for _, step := range []struct {
		sql  string
		args []any
		dest *int64
	}{
		{"INSERT INTO brands(slug,name) VALUES('diag','Diagnostics') RETURNING id", nil, &brand},
		{"INSERT INTO users(email,password_hash) VALUES('owner@diagnostic.test','sk-do-not-export-hash') RETURNING id", nil, &owner},
		{"INSERT INTO users(email,password_hash) VALUES('member@diagnostic.test','unused') RETURNING id", nil, &member},
	} {
		if err := p.QueryRow(ctx, step.sql, step.args...).Scan(step.dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Diagnostics','diag','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$3,'owner'),($2,$3,'member')", owner, member, tenant); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'diagnostic.test','active','private-verification-token',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'private','private@diagnostic.test','sk-do-not-export-hash',1024,'active')", tenant, domain); err != nil {
		t.Fatal(err)
	}
	h := &SupportHandler{Store: store.New(a), Platform: store.New(p), PlatformPool: p, DiagnosticKey: []byte(strings.Repeat("fixture-key", 4))}
	route := func(uid int64) *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(middleware.ContextUserID, uid); c.Set(middleware.ContextTenantID, tenant) })
		r.Use(middleware.RequireTenantTx(a, zap.NewNop()))
		r.POST("/diagnostics", h.Diagnostics)
		r.POST("/tickets", h.Create)
		r.GET("/attachments/:attachment_id", h.Attachment)
		return r
	}
	call := func(uid int64, method, path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		route(uid).ServeHTTP(w, req)
		return w
	}
	if w := call(owner, "POST", "/diagnostics", gin.H{"consent": false}); w.Code != 400 {
		t.Fatalf("no consent: %d %s", w.Code, w.Body)
	}
	if w := call(member, "POST", "/diagnostics", gin.H{"consent": true}); w.Code != 403 {
		t.Fatalf("member counters: %d %s", w.Code, w.Body)
	}
	w := call(owner, "POST", "/diagnostics", gin.H{"consent": true})
	if w.Code != 200 {
		t.Fatalf("preview %d %s", w.Code, w.Body)
	}
	var preview struct {
		Bundle  supportdiag.Bundle `json:"bundle"`
		Receipt string             `json:"receipt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private@", "sk-do-not-export", "private-verification-token", "password_hash", "secret"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("diagnostic leaked %q", secret)
		}
	}
	if preview.Bundle.TenantID != tenant || len(preview.Bundle.Fields) == 0 || preview.Receipt == "" {
		t.Fatal("missing tenant-bound diagnostic")
	}
	for _, field := range preview.Bundle.Fields {
		if field.Name == "config.desired_generation" || field.Name == "backup.age_hours" || field.Name == "delivery.queued_now" {
			t.Fatalf("unobserved fact emitted %+v", field)
		}
	}
	body := gin.H{"Subject": "Synchronization failure", "Body": "Synthetic sync failed; reviewed aggregate diagnostics attached.", "diagnostic_receipt": preview.Receipt}
	if w = call(member, "POST", "/tickets", body); w.Code != 400 {
		t.Fatalf("borrowed receipt %d %s", w.Code, w.Body)
	}
	body["diagnostic_receipt"] = preview.Receipt + "x"
	if w = call(owner, "POST", "/tickets", body); w.Code != 400 {
		t.Fatalf("tampered receipt %d", w.Code)
	}
	body["diagnostic_receipt"] = preview.Receipt
	w = call(owner, "POST", "/tickets", body)
	if w.Code != 201 {
		t.Fatalf("ticket %d %s", w.Code, w.Body)
	}
	var attachment int64
	var content []byte
	if err := p.QueryRow(ctx, "SELECT id,content FROM ticket_attachments WHERE tenant_id=$1 AND filename='urmail-diagnostics.json'", tenant).Scan(&attachment, &content); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(preview.Bundle)
	if !bytes.Equal(content, want) {
		t.Fatal("attachment differs from reviewed preview")
	}
	if w = call(member, "GET", fmt.Sprintf("/attachments/%d", attachment), nil); w.Code != 404 {
		t.Fatalf("same tenant member downloaded private diagnostics %d %s", w.Code, w.Body)
	}
	if w = call(owner, "GET", fmt.Sprintf("/attachments/%d", attachment), nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), want) || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("author attachment %d", w.Code)
	}
	if _, err := h.readDiagnosticReceipt(preview.Receipt, tenant+1, owner); err == nil {
		t.Fatal("cross-tenant diagnostic proof accepted")
	}
	var tickets int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM tickets WHERE tenant_id=$1", tenant).Scan(&tickets); err != nil || tickets != 1 {
		t.Fatalf("invalid proof wrote orphan ticket: %d %v", tickets, err)
	}
}
