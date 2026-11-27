package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestSQLiteIMAPListingsLeaveTheGate(t *testing.T) {
	_, server := newSlice1Server(t)
	client := &slice1Client{t: t, server: server}
	const password = "slice1-mailbox-password"

	// Unauthenticated requests keep 401.
	for _, path := range []string{
		"/api/v1/mailbox/folders",
		"/api/v1/mailbox/messages",
		"/api/v1/mailbox/folders/INBOX/messages",
	} {
		if code, _ := client.do(http.MethodGet, path, ""); code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated GET %s = %d, want 401", path, code)
		}
	}

	// Real SQLite login for alice.
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

	// Slice-1.5: the read-only listing surfaces left the gate (TC-13a fix).
	// Listings are IMAP-backed now: the harness points IMAPAddr at an
	// unreachable port, so the handlers must fail LOUDLY at the IMAP
	// boundary (502 IMAP_UNAVAILABLE) — never the gate 503 and never a
	// silently empty list.
	for _, path := range []string{
		"/api/v1/mailbox/folders",
		"/api/v1/mailbox/messages",
		"/api/v1/mailbox/folders/INBOX/messages",
		"/api/v1/mailbox/folders/NoSuchFolder/messages",
	} {
		code, body = client.do(http.MethodGet, path, "")
		if code != http.StatusBadGateway || !strings.Contains(body, "IMAP_UNAVAILABLE") {
			t.Fatalf("GET %s = %d %s, want 502 IMAP_UNAVAILABLE", path, code, body)
		}
	}

	// Write operations remain strictly blocked with 503 FEATURE_UNAVAILABLE.
	for _, writeReq := range []struct {
		method, path, payload string
	}{
		{http.MethodPost, "/api/v1/mailbox/folders", `{"name":"Custom"}`},
		{http.MethodPatch, "/api/v1/mailbox/folders/INBOX", `{"name":"Renamed","uid_validity":1}`},
		{http.MethodDelete, "/api/v1/mailbox/folders/INBOX?uid_validity=1", ""},
		// slice-3: drafts left the gate; their handler-level failures are
		// asserted in TestSlice3GateMatrix.
		{http.MethodPost, "/api/v1/mailbox/folders/INBOX/messages/delete", `{"uids":[1],"uid_validity":1}`},
	} {
		code, body = client.do(writeReq.method, writeReq.path, writeReq.payload)
		if code != http.StatusServiceUnavailable || !strings.Contains(body, "FEATURE_UNAVAILABLE") {
			t.Fatalf("write %s %s = %d %s, want 503 FEATURE_UNAVAILABLE", writeReq.method, writeReq.path, code, body)
		}
	}
}

func TestSQLiteListingsServeIMAPNotSeededRows(t *testing.T) {
	db, server := newSlice1Server(t)
	ctx := context.Background()
	const password = "slice1-mailbox-password"

	// Seed persisted rows directly into the migrated mailbox_folders / mailbox_messages tables
	// (no ad-hoc DDL; both tables come from migrations_sqlite/0003_mailbox_readonly.sql).
	for _, stmt := range []string{
		`INSERT INTO mailbox_folders(tenant_id,mailbox_id,name,delimiter,flags_json) VALUES
			(7,11,'INBOX','/','["\\HasNoChildren"]'),
			(7,11,'Archive','/','["\\HasNoChildren","\\Archive"]')`,
		`INSERT INTO mailbox_messages(tenant_id,mailbox_id,folder,uid_validity,uid,subject,from_name,from_email,date_micros,flags_json,size_bytes,has_attachments,preview,message_id) VALUES
			(7,11,'INBOX',42,101,'Alice Welcome','System','sys@a.example.test',1737412800000000,'["\\Seen"]',512,0,'Welcome alice','<m1@a.example.test>'),
			(8,12,'INBOX',99,201,'Bob Private','BobSender','sender@b.example.test',1737413000000000,'[]',256,0,'Bob only','<m3@b.example.test>')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	loginAs := func(email string) *slice1Client {
		t.Helper()
		c := &slice1Client{t: t, server: server}
		code, body := c.do(http.MethodPost, "/api/v1/mailbox/session",
			`{"email":"`+email+`","password":"`+password+`"}`)
		if code != http.StatusOK {
			t.Fatalf("login %s = %d %s", email, code, body)
		}
		var s struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(body), &s); err != nil || s.Token == "" {
			t.Fatalf("login decode %s: %v %s", email, err, body)
		}
		c.token = s.Token
		return c
	}

	alice := loginAs("alice@a.example.test")
	bob := loginAs("bob@b.example.test")

	// Seeded rows change nothing at the HTTP layer: listings are IMAP-backed
	// in SQLite mode and never read the SQLite index tables, so the seeded
	// rows cannot leak through a partial unmigrated view (QA TC-13a). With
	// the harness's unreachable IMAP endpoint the handlers fail loudly with
	// 502 IMAP_UNAVAILABLE instead.
	for _, tc := range []struct {
		c    *slice1Client
		path string
	}{
		{alice, "/api/v1/mailbox/folders"},
		{alice, "/api/v1/mailbox/messages?q=Welcome"},
		{alice, "/api/v1/mailbox/folders/INBOX/messages"},
		{bob, "/api/v1/mailbox/folders"},
		{bob, "/api/v1/mailbox/messages"},
	} {
		if code, body := tc.c.do(http.MethodGet, tc.path, ""); code != http.StatusBadGateway || !strings.Contains(body, "IMAP_UNAVAILABLE") {
			t.Fatalf("GET %s = %d %s, want 502 IMAP_UNAVAILABLE", tc.path, code, body)
		}
	}

	// Brand boundary still rejects cross-brand token replay at the auth
	// layer (before the gate).
	if code, _ := alice.doHost(http.MethodGet, "/api/v1/mailbox/folders", "", "acme.example.test"); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("cross-brand folder read = %d, want 401/403", code)
	}

	// Direct adapter misuse with mismatched (tenantID, mailboxID) still
	// fails closed at the storage layer (the adapter stays ownership-checked
	// even though listings no longer route through it).
	probe := handlers.SQLiteMailboxStore{DB: db}
	if _, err := probe.ListFolders(ctx, 8, 11); !errors.Is(err, handlers.ErrMailboxNotFound) {
		t.Fatalf("cross-tenant ListFolders err=%v, want ErrMailboxNotFound", err)
	}
	if _, err := probe.ListMessages(ctx, 7, 12, "INBOX", webmail.ListOpts{}); !errors.Is(err, handlers.ErrMailboxNotFound) {
		t.Fatalf("cross-mailbox ListMessages err=%v, want ErrMailboxNotFound", err)
	}
}
