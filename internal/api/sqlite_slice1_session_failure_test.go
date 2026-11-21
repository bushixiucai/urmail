package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
)

func TestSlice1SQLiteBrandLookupFailureDoesNotIssueSessionCredential(t *testing.T) {
	db, server := newSlice1Server(t)
	ctx := context.Background()
	var brandHostsDDL string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='brand_hosts'`).Scan(&brandHostsDDL); err != nil || brandHostsDDL == "" {
		t.Fatalf("read brand_hosts DDL: err=%v", err)
	}
	var before int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_sessions`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE brand_hosts`); err != nil {
		t.Fatalf("drop brand_hosts for failure injection: %v", err)
	}

	login := func() (int, map[string]json.RawMessage, []*http.Cookie) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/mailbox/session",
			strings.NewReader(`{"email":"dave@d.acme.example.test","password":"slice1-mailbox-password"}`))
		if err != nil {
			t.Fatalf("create login request: %v", err)
		}
		req.Host = "acme.example.test"
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("send login request: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read login response: %v", err)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("login response is not JSON (status %d)", resp.StatusCode)
		}
		return resp.StatusCode, payload, resp.Cookies()
	}

	code, failure, cookies := login()
	if code != http.StatusServiceUnavailable {
		t.Fatalf("failed brand lookup status=%d, want 503", code)
	}
	if _, present := failure["token"]; present {
		t.Fatal("failed login response contains a session token field")
	}
	if len(cookies) != 0 {
		t.Fatalf("failed login set %d cookies, want none", len(cookies))
	}
	var afterFailure int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_sessions`).Scan(&afterFailure); err != nil || afterFailure != before {
		t.Fatalf("failed login changed session rows: before=%d after=%d err=%v", before, afterFailure, err)
	}

	if _, err := db.ExecContext(ctx, brandHostsDDL); err != nil {
		t.Fatalf("restore brand_hosts DDL: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO brand_hosts(hostname,brand_id,status) VALUES('acme.example.test',9,'active')`); err != nil {
		t.Fatalf("restore healthy brand fixture: %v", err)
	}
	code, healthy, cookies := login()
	if code != http.StatusOK {
		t.Fatalf("healthy login status=%d, want 200", code)
	}
	var token string
	if err := json.Unmarshal(healthy["token"], &token); err != nil || token == "" {
		t.Fatal("healthy login did not return a session token")
	}
	if len(cookies) != 0 {
		t.Fatalf("healthy bearer-token login set %d cookies, want none", len(cookies))
	}
	var afterHealthy int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_sessions`).Scan(&afterHealthy); err != nil || afterHealthy != before+1 {
		t.Fatalf("healthy login did not create exactly one session: before=%d after=%d err=%v", before, afterHealthy, err)
	}
	var tokenRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_sessions WHERE token_hash=?`, mailboxprincipal.HashToken(token)).Scan(&tokenRows); err != nil || tokenRows != 1 {
		t.Fatalf("healthy token does not identify exactly one stored session: rows=%d err=%v", tokenRows, err)
	}
}
