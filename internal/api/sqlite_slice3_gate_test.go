package api_test

import (
	"context"
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
	db, server := newSlice1Server(t)
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
		// Slice-1.5 moved the read-only listing/read surfaces out of the
		// gate (IMAP-backed listings landed); slice-1.6 moved the image
		// proxy out (sanitizer-signed, self-authorizing); b5 moved search,
		// flags, folder writes, the scheduled-sends/send-receipts families
		// and password out; b6 moved the corporate-directory read surface
		// out (slice store, application-layer tenant scoping). Still gated:
		// the message mutation family (move/copy/trash/restore/delete/
		// import — not part of b5/b6).
		{http.MethodPost, "/api/v1/mailbox/folders/INBOX/messages/move", `{"uids":[1],"to":"Archive","uid_validity":1}`},
		{http.MethodPost, "/api/v1/mailbox/folders/INBOX/messages/copy", `{"uids":[1],"to":"Archive","uid_validity":1}`},
		{http.MethodPost, "/api/v1/mailbox/folders/INBOX/messages/trash", `{"uids":[1],"uid_validity":1}`},
		{http.MethodPost, "/api/v1/mailbox/folders/INBOX/messages/restore", `{"uids":[1],"uid_validity":1}`},
		{http.MethodPost, "/api/v1/mailbox/folders/INBOX/messages/delete", `{"uids":[1],"uid_validity":1}`},
		{http.MethodPost, "/api/v1/mailbox/folders/INBOX/import", "raw"},
	} {
		code, body = client.do(tc.method, tc.path, tc.payload)
		if code != http.StatusServiceUnavailable || !strings.Contains(body, "FEATURE_UNAVAILABLE") {
			t.Fatalf("gated %s %s = %d %s, want 503 FEATURE_UNAVAILABLE", tc.method, tc.path, code, body)
		}
	}

	// Approved family: the gate must NOT answer — against the fixture the
	// SMTP/IMAP dials are unreachable by construction, so send/drafts fail
	// with their handlers' transport errors (no fake MTA here), and rules
	// hits its own rulesReady state. None of them may carry
	// FEATURE_UNAVAILABLE.
	for _, tc := range []struct {
		method, path, payload string
	}{
		{http.MethodPost, "/api/v1/mailbox/send", `{"to":["bob@b.example.test"],"subject":"s3","text":"hi"}`},
		{http.MethodPut, "/api/v1/mailbox/drafts", `{"to":["bob@b.example.test"],"subject":"d","text":"body"}`},
		{http.MethodGet, "/api/v1/mailbox/drafts", ""},
		// S3-1: the uid sub-route carries the family DELETE; it must leave
		// the gate with its siblings (QA round 1 caught it still gated).
		{http.MethodDelete, "/api/v1/mailbox/drafts/42?uid_validity=42", ""},
		{http.MethodGet, "/api/v1/mailbox/rules", ""},
		{http.MethodPut, "/api/v1/mailbox/rules", `{"version":"","rules":[]}`},
		// Slice-1.5: the read-only listing/read surfaces left the gate —
		// against the unreachable-IMAP fixture they fail loudly at the
		// handler boundary (502 IMAP_UNAVAILABLE), never the gate 503.
		{http.MethodGet, "/api/v1/mailbox/folders", ""},
		{http.MethodGet, "/api/v1/mailbox/messages", ""},
		{http.MethodGet, "/api/v1/mailbox/folders/INBOX/messages", ""},
		{http.MethodGet, "/api/v1/mailbox/folders/INBOX/messages/1", ""},
		// Slice-1.6: the image proxy left the gate — an unsigned request
		// reaches the handler, which rejects it with its own 403
		// (MAILBOX_AUTHORIZATION_DENIED), never the gate 503.
		{http.MethodGet, "/api/v1/mailbox/images/proxy?u=aHR0cDovL2V4YW1wbGUuY2xpcA==&sig=badsig", ""},
	} {
		code, body = client.do(tc.method, tc.path, tc.payload)
		if code == http.StatusServiceUnavailable && strings.Contains(body, "FEATURE_UNAVAILABLE") {
			t.Fatalf("approved %s %s still answered the gate: %d %s", tc.method, tc.path, code, body)
		}
	}

	// b5 batch: the six newly approved doors must never answer the gate.
	// Expected semantics are asserted exactly:
	//   - IMAP-backed doors (search, flags, folder writes) fail loudly at
	//     the IMAP boundary — 502 IMAP_UNAVAILABLE against the unreachable
	//     fixture, never a silent mutation and never FEATURE_UNAVAILABLE;
	//   - platform-backed doors (scheduled-sends, send-receipts, password)
	//     reach their handlers and answer the fail-closed 503
	//     WEBMAIL_DISABLED while the slice runtime has no PG pool.
	for _, tc := range []struct {
		method, path, payload string
		wantCode              int
		wantBody              string
	}{
		{http.MethodGet, "/api/v1/mailbox/search?q=probe", "", http.StatusBadGateway, "IMAP_UNAVAILABLE"},
		{http.MethodPost, "/api/v1/mailbox/folders/INBOX/messages/flags", `{"add":["\\Seen"],"remove":[],"uids":[1],"uid_validity":1}`, http.StatusBadGateway, "IMAP_UNAVAILABLE"},
		{http.MethodPost, "/api/v1/mailbox/folders", `{"name":"Custom"}`, http.StatusBadGateway, "IMAP_UNAVAILABLE"},
		{http.MethodPatch, "/api/v1/mailbox/folders/INBOX", `{"name":"Renamed","uid_validity":1}`, http.StatusBadGateway, "IMAP_UNAVAILABLE"},
		{http.MethodDelete, "/api/v1/mailbox/folders/INBOX?uid_validity=1", "", http.StatusBadGateway, "IMAP_UNAVAILABLE"},
		{http.MethodPost, "/api/v1/mailbox/scheduled-sends", `{"to":["bob@b.example.test"],"subject":"s5","text":"hi","send_at":"2030-01-01T00:00:00Z"}`, http.StatusServiceUnavailable, "WEBMAIL_DISABLED"},
		{http.MethodGet, "/api/v1/mailbox/scheduled-sends", "", http.StatusServiceUnavailable, "WEBMAIL_DISABLED"},
		{http.MethodDelete, "/api/v1/mailbox/scheduled-sends/42", "", http.StatusServiceUnavailable, "WEBMAIL_DISABLED"},
		{http.MethodPost, "/api/v1/mailbox/scheduled-sends/42/repair-sent-copy", "", http.StatusServiceUnavailable, "WEBMAIL_DISABLED"},
		{http.MethodPost, "/api/v1/mailbox/scheduled-sends/42/acknowledge-uncertain", "", http.StatusServiceUnavailable, "WEBMAIL_DISABLED"},
		// pm-webmail-writepath-1: the receipts GET surface is functional
		// from the SQLite ledger — the fixture seeds none, so it answers
		// 200 with an empty list, never the gate 503. The repair POST
		// needs the sealing secrets (absent in this fixture) and keeps
		// the fail-closed 503 WEBMAIL_DISABLED.
		{http.MethodGet, "/api/v1/mailbox/send-receipts", "", http.StatusOK, `"receipts":[]`},
		{http.MethodGet, "/api/v1/mailbox/send-receipts/42", "", http.StatusNotFound, "NOT_FOUND"},
		{http.MethodPost, "/api/v1/mailbox/send-receipts/42/repair-sent-copy", "", http.StatusServiceUnavailable, "WEBMAIL_DISABLED"},
		{http.MethodPost, "/api/v1/mailbox/password", `{"old_password":"slice1-mailbox-password","new_password":"another-valid-password-1"}`, http.StatusServiceUnavailable, "WEBMAIL_DISABLED"},
	} {
		code, body = client.do(tc.method, tc.path, tc.payload)
		if code != tc.wantCode || !strings.Contains(body, tc.wantBody) {
			t.Fatalf("b5 approved %s %s = %d %s, want %d %s", tc.method, tc.path, code, body, tc.wantCode, tc.wantBody)
		}
	}

	// b6: the corporate-directory read surface left the gate and is
	// functionally served by the SQLite slice store. The fixture seeds no
	// directory settings, so the endpoints answer 200 with an empty,
	// disabled payload — never the gate 503.
	for _, tc := range []struct {
		method, path, wantBody string
	}{
		{http.MethodGet, "/api/v1/mailbox/corporate-directory", `"enabled":false`},
		{http.MethodGet, "/api/v1/mailbox/corporate-directory/search?q=alice", `"contacts":[]`},
	} {
		code, body = client.do(tc.method, tc.path, "")
		if code != http.StatusOK || !strings.Contains(body, tc.wantBody) {
			t.Fatalf("b6 approved %s %s = %d %s, want 200 %s", tc.method, tc.path, code, body, tc.wantBody)
		}
	}

	// pm-readunlock-1 + pm-webmail-writepath-1: the two read doors are
	// bound to the AUTHENTICATED principal. search hits each session's own
	// IMAP boundary; send-receipts is functional from the SQLite ledger —
	// and the isolation teeth stay red-sensitive: alice owns a seeded
	// ledger row, and bob's owner-scoped list must NEVER carry it.
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO mailbox_send_ledger("tenant_id","mailbox_id","request_key","request_hash","status")
		 VALUES(7,11,'alice-pin-receipt','hash-pin','accepted')`); err != nil {
		t.Fatalf("seed alice ledger row: %v", err)
	}
	bob := &slice1Client{t: t, server: server}
	code, body = bob.do(http.MethodPost, "/api/v1/mailbox/session",
		`{"email":"bob@b.example.test","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("bob login = %d %s", code, body)
	}
	var bobSession struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &bobSession); err != nil || bobSession.Token == "" {
		t.Fatalf("bob session parse: %v %s", err, body)
	}
	bob.token = bobSession.Token
	for _, tc := range []struct {
		method, path string
		wantCode     int
		wantBody     string
	}{
		{http.MethodGet, "/api/v1/mailbox/search?q=probe", http.StatusBadGateway, "IMAP_UNAVAILABLE"},
	} {
		code, body = bob.do(tc.method, tc.path, "")
		if code != tc.wantCode || !strings.Contains(body, tc.wantBody) {
			t.Fatalf("bob %s %s = %d %s, want %d %s", tc.method, tc.path, code, body, tc.wantCode, tc.wantBody)
		}
	}
	// bob's owner-scoped receipts: 200, only his own (none here) — alice's
	// seeded row must not appear, and a foreign id must not resolve.
	code, body = bob.do(http.MethodGet, "/api/v1/mailbox/send-receipts", "")
	if code != http.StatusOK || !strings.Contains(body, `"receipts":[]`) || strings.Contains(body, "alice-pin-receipt") {
		t.Fatalf("bob receipts = %d %s, want own-only empty list", code, body)
	}
	code, body = bob.do(http.MethodGet, "/api/v1/mailbox/send-receipts/1", "")
	if code == http.StatusOK && strings.Contains(body, "alice-pin-receipt") {
		t.Fatalf("bob resolved a foreign receipt: %s", body)
	}
	// alice's owner-scoped list carries her seeded row (owner view intact).
	code, body = client.do(http.MethodGet, "/api/v1/mailbox/send-receipts", "")
	if code != http.StatusOK || !strings.Contains(body, "alice-pin-receipt") {
		t.Fatalf("alice receipts = %d %s, want her seeded row", code, body)
	}

	// Unauthenticated callers keep 401 on the newly approved routes.
	fresh := &slice1Client{t: t, server: server}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/mailbox/send"},
		{http.MethodPut, "/api/v1/mailbox/drafts"},
		{http.MethodGet, "/api/v1/mailbox/rules"},
		// b5 doors: the gate runs AFTER authentication, so callers without
		// a mailbox session keep the original 401, never a 503.
		{http.MethodGet, "/api/v1/mailbox/search?q=probe"},
		{http.MethodPost, "/api/v1/mailbox/folders"},
		{http.MethodPatch, "/api/v1/mailbox/folders/INBOX"},
		{http.MethodDelete, "/api/v1/mailbox/folders/INBOX?uid_validity=1"},
		{http.MethodPost, "/api/v1/mailbox/folders/INBOX/messages/flags"},
		{http.MethodPost, "/api/v1/mailbox/scheduled-sends"},
		{http.MethodGet, "/api/v1/mailbox/scheduled-sends"},
		{http.MethodDelete, "/api/v1/mailbox/scheduled-sends/42"},
		{http.MethodGet, "/api/v1/mailbox/send-receipts"},
		{http.MethodGet, "/api/v1/mailbox/send-receipts/42"},
		{http.MethodPost, "/api/v1/mailbox/password"},
		// b6 doors.
		{http.MethodGet, "/api/v1/mailbox/corporate-directory"},
		{http.MethodGet, "/api/v1/mailbox/corporate-directory/search?q=alice"},
	} {
		if code, _ := fresh.do(tc.method, tc.path, "{}"); code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s = %d, want 401", tc.method, tc.path, code)
		}
	}
}
