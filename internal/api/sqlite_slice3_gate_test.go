package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestSlice3GateMatrix is the S3 allow/deny acceptance matrix
// (S3-UNGATE-FREEZE-PLAN §3.1): send/drafts/rules leave the gate (send
// completes a real submission against the fake MTA; drafts and rules reach
// their handlers, which answer their own explicit unavailability/protocol
// errors — never the gate 503), while listings, single-message reads, the
// image proxy and corporate-directory stay explicitly gated, and
// unauthenticated callers keep 401.
func TestSlice3GateMatrix(t *testing.T) {
	_, server := newSlice1Server(t)
	client := &slice1Client{t: t, server: server}
	const password = "slice1-mailbox-password"
	code, body := client.do(http.MethodPost, "/api/v1/mailbox/session",
		`{"email":"alice@a.example.test","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("alice login = %d %s", code, body)
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &session); err != nil || session.Token == "" {
		t.Fatalf("login payload = %q (%v)", body, err)
	}
	client.token = session.Token

	// Keep-gated family: exactly the gate 503 FEATURE_UNAVAILABLE.
	for _, tc := range []struct {
		method, path, payload string
	}{
		{http.MethodGet, "/api/v1/mailbox/folders", ""},
		{http.MethodGet, "/api/v1/mailbox/messages", ""},
		{http.MethodGet, "/api/v1/mailbox/folders/INBOX/messages", ""},
		{http.MethodGet, "/api/v1/mailbox/folders/INBOX/messages/1", ""},
		{http.MethodGet, "/api/v1/mailbox/images/proxy?u=aHR0cDovL2V4YW1wbGUuY2xpcA==", ""},
		{http.MethodGet, "/api/v1/mailbox/corporate-directory/search?q=alice", ""},
	} {
		code, body = client.do(tc.method, tc.path, tc.payload)
		if code != http.StatusServiceUnavailable || !strings.Contains(body, "FEATURE_UNAVAILABLE") {
			t.Fatalf("gated %s %s = %d %s, want 503 FEATURE_UNAVAILABLE", tc.method, tc.path, code, body)
		}
	}

	// Approved family: the gate must NOT answer — send against the
	// unreachable fixture SMTP is a transport failure (non-gate 5xx), drafts
	// against the unreachable IMAP likewise, and rules hits the handler's
	// own rulesReady state. None of them may carry FEATURE_UNAVAILABLE.
	for _, tc := range []struct {
		method, path, payload string
	}{
		{http.MethodPost, "/api/v1/mailbox/send", `{"to":["bob@b.example.test"],"subject":"s3","text":"hi"}`},
		{http.MethodPut, "/api/v1/mailbox/drafts", `{"to":["bob@b.example.test"],"subject":"d","text":"body"}`},
		{http.MethodGet, "/api/v1/mailbox/drafts", ""},
		{http.MethodGet, "/api/v1/mailbox/rules", ""},
		{http.MethodPut, "/api/v1/mailbox/rules", `{"version":"","rules":[]}`},
	} {
		code, body = client.do(tc.method, tc.path, tc.payload)
		if code == http.StatusServiceUnavailable && strings.Contains(body, "FEATURE_UNAVAILABLE") {
			t.Fatalf("approved %s %s still answered the gate: %d %s", tc.method, tc.path, code, body)
		}
	}

	// Unauthenticated callers keep 401 on the newly approved routes.
	fresh := &slice1Client{t: t, server: server}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/mailbox/send"},
		{http.MethodPut, "/api/v1/mailbox/drafts"},
		{http.MethodGet, "/api/v1/mailbox/rules"},
	} {
		if code, _ := fresh.do(tc.method, tc.path, "{}"); code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s = %d, want 401", tc.method, tc.path, code)
		}
	}
}
