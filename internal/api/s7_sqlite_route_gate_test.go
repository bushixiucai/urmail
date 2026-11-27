package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestSQLiteRouteGateRejectsUnsupportedPaths uses the real api.New SQLite
// server and a real mailbox session: every non-whitelisted mailbox route must
// answer FEATURE_UNAVAILABLE before any dial/secret/data effect, while a
// whitelisted route still works.
func TestSQLiteRouteGateRejectsUnsupportedPaths(t *testing.T) {
	db, server := newSlice1Server(t)
	client := &slice1Client{t: t, server: server}
	ctx := context.Background()
	code, body := client.do(http.MethodPost, "/api/v1/mailbox/session", `{"email":"alice@a.example.test","password":"slice1-mailbox-password"}`)
	if code != http.StatusOK {
		t.Fatalf("login=%d %s", code, body)
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := jsonUnmarshal(body, &session); err != nil || session.Token == "" {
		t.Fatalf("session parse: %v %s", err, body)
	}
	client.token = session.Token
	counts := func() (int64, int64, int64) {
		var receipts, schedules, sessions int64
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM mailbox_send_receipts").Scan(&receipts); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM mailbox_scheduled_sends").Scan(&schedules); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM mailbox_sessions").Scan(&sessions); err != nil {
			t.Fatal(err)
		}
		return receipts, schedules, sessions
	}
	beforeR, beforeS, beforeSess := counts()
	for _, probe := range []struct{ method, path, body string }{
		// slice-3 (this change): send and the whole drafts family (PUT/GET
		// collection + DELETE /drafts/:uid, S3-1) plus rules left the deny
		// list — their allow-side assertions live in TestSlice3GateMatrix.
		// b5: search, flags, folder writes, the scheduled-sends/send-receipts
		// families and password also left the deny list. Still denied with
		// the gate FEATURE_UNAVAILABLE: the message mutation family and
		// corporate-directory.
		{http.MethodPost, "/api/v1/mailbox/folders/INBOX/messages/move", `{"uids":[1],"to":"Archive","uid_validity":1}`},
		{http.MethodPost, "/api/v1/mailbox/folders/INBOX/messages/trash", `{"uids":[1],"uid_validity":1}`},
		{http.MethodGet, "/api/v1/mailbox/corporate-directory/search?q=alice", ""},
	} {
		code, body := client.do(probe.method, probe.path, probe.body)
		if code != http.StatusServiceUnavailable || !contains(body, "FEATURE_UNAVAILABLE") {
			t.Fatalf("%s %s = %d %s, want 503 FEATURE_UNAVAILABLE", probe.method, probe.path, code, body)
		}
	}
	afterR, afterS, afterSess := counts()
	if beforeR != afterR || beforeS != afterS || beforeSess != afterSess {
		t.Fatalf("gate wrote data: receipts %d->%d schedules %d->%d sessions %d->%d", beforeR, afterR, beforeS, afterS, beforeSess, afterSess)
	}

	// b5: the platform-backed doors left the gate; with no PG pool in the
	// slice runtime their handlers answer the fail-closed 503
	// WEBMAIL_DISABLED — and must still write nothing (the original S7
	// side-effect contract carries over to the new semantics).
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/mailbox/send-receipts", ""},
		{http.MethodPost, "/api/v1/mailbox/scheduled-sends", `{"to":["x@example.test"],"text":"x"}`},
		{http.MethodPost, "/api/v1/mailbox/password", `{"old_password":"x","new_password":"another-valid-password-1"}`},
	} {
		code, body := client.do(probe.method, probe.path, probe.body)
		if code != http.StatusServiceUnavailable || !contains(body, "WEBMAIL_DISABLED") {
			t.Fatalf("%s %s = %d %s, want 503 WEBMAIL_DISABLED (fail-closed, no PG pool)", probe.method, probe.path, code, body)
		}
	}
	afterR2, afterS2, afterSess2 := counts()
	if beforeR != afterR2 || beforeS != afterS2 || beforeSess != afterSess2 {
		t.Fatalf("handler wrote data while disabled: receipts %d->%d schedules %d->%d sessions %d->%d", beforeR, afterR2, beforeS, afterS2, beforeSess, afterSess2)
	}
	if code, body := client.do(http.MethodGet, "/api/v1/mailbox/settings", ""); code != http.StatusOK {
		t.Fatalf("whitelisted settings route = %d %s", code, body)
	}
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
func contains(s, sub string) bool         { return strings.Contains(s, sub) }
