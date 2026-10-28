package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestSupportDiagnosticProductionJourneyPG(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	ctx := t.Context()
	password := "Synthetic-support-browser-only!42"
	hash, err := panelpassword.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	var brand, tenant, user int64
	for _, step := range []struct {
		query string
		args  []any
		id    *int64
	}{
		{"INSERT INTO brands(slug,name) VALUES('default','Support journey') RETURNING id", nil, &brand},
		{"INSERT INTO users(email,password_hash) VALUES('support-owner@example.test',$1) RETURNING id", []any{hash}, &user},
	} {
		if err = platform.QueryRow(ctx, step.query, step.args...).Scan(step.id); err != nil {
			t.Fatal(err)
		}
	}
	if err = platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Support journey','support-journey','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", user, tenant); err != nil {
		t.Fatal(err)
	}
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	engine := New(Options{PlatformPool: platform, AppPool: app, Logger: zap.NewNop(), Messages: messages, QuoteHMACKey: strings.Repeat("synthetic-browser-key", 3), MyHostname: "mail.support-fixture.test", WorkersHeld: true})
	var token, csrf string
	call := func(method, path string, body any, withCSRF bool) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		request := httptest.NewRequestWithContext(ctx, method, path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		if withCSRF {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		return response
	}
	login := call("POST", "/api/v1/auth/login", map[string]string{"email": "support-owner@example.test", "password": password}, false)
	var session struct {
		Token string `json:"token"`
		CSRF  string `json:"csrf_token"`
	}
	if login.Code != 200 || json.Unmarshal(login.Body.Bytes(), &session) != nil || session.Token == "" {
		t.Fatalf("real login %d %s", login.Code, login.Body)
	}
	token, csrf = session.Token, session.CSRF
	if response := call("POST", "/api/v1/support/diagnostics", map[string]bool{"consent": true}, false); response.Code != 403 {
		t.Fatalf("missing CSRF accepted %d %s", response.Code, response.Body)
	}
	preview := call("POST", "/api/v1/support/diagnostics", map[string]bool{"consent": true}, true)
	var diagnostic struct {
		Receipt string `json:"receipt"`
	}
	if preview.Code != 200 || json.Unmarshal(preview.Body.Bytes(), &diagnostic) != nil || diagnostic.Receipt == "" {
		t.Fatalf("preview %d %s", preview.Code, preview.Body)
	}
	created := call("POST", "/api/v1/support/tickets", map[string]string{"Subject": "Synthetic calendar sync failure", "Body": "The synthetic client received a synchronization failure and followed the calendar help guide.", "diagnostic_receipt": diagnostic.Receipt}, true)
	var ticket struct {
		Ticket struct {
			ID int64 `json:"id"`
		} `json:"ticket"`
	}
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &ticket) != nil || ticket.Ticket.ID == 0 {
		t.Fatalf("ticket %d %s", created.Code, created.Body)
	}
	detail := call("GET", fmt.Sprintf("/api/v1/support/tickets/%d", ticket.Ticket.ID), nil, false)
	if detail.Code != 200 || !bytes.Contains(detail.Body.Bytes(), []byte("urmail-diagnostics.json")) {
		t.Fatalf("ticket attachment %d %s", detail.Code, detail.Body)
	}
	if directory := os.Getenv("URMAIL_SUPPORT_BROWSER_DIR"); directory != "" {
		if err = os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(engine)
		defer server.Close()
		fixture, _ := json.Marshal(map[string]any{"url": server.URL, "email": "support-owner@example.test", "password": password})
		if err = os.WriteFile(filepath.Join(directory, "fixture.json"), fixture, 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("Real production auth, CSRF, support API and PostgreSQL browser fixture ready")
		deadline := time.Now().Add(15 * time.Minute)
		for time.Now().Before(deadline) {
			if result, e := os.ReadFile(filepath.Join(directory, "browser.done")); e == nil {
				if strings.TrimSpace(string(result)) != "PASS" {
					t.Fatal("browser failed")
				}
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
		t.Fatal("support browser fixture timed out")
	}
}
