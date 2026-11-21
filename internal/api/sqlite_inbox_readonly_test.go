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

func TestSQLiteReadOnlyFoldersAndMessagesEmptyMailbox(t *testing.T) {
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

	// Real SQLite login for alice (empty mailbox: only 0001+0002 schema).
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

	// Folders return 200 with standard default folders and encoded names.
	code, body = client.do(http.MethodGet, "/api/v1/mailbox/folders", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/mailbox/folders = %d %s", code, body)
	}
	var foldersResp struct {
		Folders []webmail.Folder `json:"folders"`
	}
	if err := json.Unmarshal([]byte(body), &foldersResp); err != nil || len(foldersResp.Folders) != 5 {
		t.Fatalf("unexpected folders payload: %s (%v)", body, err)
	}
	inboxEnc := ""
	for _, f := range foldersResp.Folders {
		if f.Name == "INBOX" {
			inboxEnc = f.NameEnc
		}
		if f.NameEnc == "" {
			t.Fatalf("folder %q missing name_enc", f.Name)
		}
	}
	if inboxEnc == "" {
		t.Fatalf("INBOX missing from default folders: %+v", foldersResp.Folders)
	}

	// Empty INBOX via /folders/:name/messages and /messages both return 200 + empty array.
	for _, path := range []string{
		"/api/v1/mailbox/folders/" + inboxEnc + "/messages",
		"/api/v1/mailbox/messages",
	} {
		code, body = client.do(http.MethodGet, path, "")
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, code, body)
		}
		var listResp webmail.ListResult
		if err := json.Unmarshal([]byte(body), &listResp); err != nil {
			t.Fatalf("decode %s: %v (%s)", path, err, body)
		}
		if listResp.Total != 0 || len(listResp.Messages) != 0 || listResp.UIDValidity == 0 {
			t.Fatalf("unexpected empty list for %s: %+v", path, listResp)
		}
	}

	// Unknown folder fails closed with 404.
	if code, _ := client.do(http.MethodGet, "/api/v1/mailbox/folders/NoSuchFolder/messages", ""); code != http.StatusNotFound {
		t.Fatalf("unknown folder = %d, want 404", code)
	}

	// Write operations remain strictly blocked with 503 FEATURE_UNAVAILABLE.
	for _, writeReq := range []struct {
		method, path, payload string
	}{
		{http.MethodPost, "/api/v1/mailbox/folders", `{"name":"Custom"}`},
		{http.MethodPatch, "/api/v1/mailbox/folders/" + inboxEnc, `{"name":"Renamed","uid_validity":1}`},
		{http.MethodDelete, "/api/v1/mailbox/folders/" + inboxEnc + "?uid_validity=1", ""},
		{http.MethodPut, "/api/v1/mailbox/drafts", `{"to":["bob@b.example.test"],"subject":"hi","text":"body"}`},
		{http.MethodPost, "/api/v1/mailbox/folders/" + inboxEnc + "/messages/delete", `{"uids":[1],"uid_validity":1}`},
	} {
		code, body = client.do(writeReq.method, writeReq.path, writeReq.payload)
		if code != http.StatusServiceUnavailable || !strings.Contains(body, "FEATURE_UNAVAILABLE") {
			t.Fatalf("write %s %s = %d %s, want 503 FEATURE_UNAVAILABLE", writeReq.method, writeReq.path, code, body)
		}
	}
}

func TestSQLiteReadOnlyFoldersAndMessagesSeededAndIsolated(t *testing.T) {
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
			(7,11,'INBOX',42,102,'Alice Secret','Partner','partner@a.example.test',1737412900000000,'[]',1024,1,'Confidential','<m2@a.example.test>'),
			(8,12,'INBOX',99,201,'Bob Private','BobSender','sender@b.example.test',1737413000000000,'[]',256,0,'Bob only','<m3@b.example.test>')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	loginAs := func(email, host string) *slice1Client {
		t.Helper()
		c := &slice1Client{t: t, server: server}
		code, body := c.doHost(http.MethodPost, "/api/v1/mailbox/session",
			`{"email":"`+email+`","password":"`+password+`"}`, host)
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

	alice := loginAs("alice@a.example.test", "")
	bob := loginAs("bob@b.example.test", "")
	carol := loginAs("carol@a.example.test", "")

	// Alice sees her seeded Archive folder and only her 2 INBOX messages.
	code, body := alice.do(http.MethodGet, "/api/v1/mailbox/folders", "")
	if code != http.StatusOK || !strings.Contains(body, "Archive") {
		t.Fatalf("alice folders = %d %s", code, body)
	}
	code, body = alice.do(http.MethodGet, "/api/v1/mailbox/messages?q=Secret", "")
	var aliceList webmail.ListResult
	if code != http.StatusOK || json.Unmarshal([]byte(body), &aliceList) != nil || aliceList.Total != 1 || aliceList.Messages[0].Subject != "Alice Secret" {
		t.Fatalf("alice filtered messages = %d %s", code, body)
	}
	if strings.Contains(body, "Bob Private") {
		t.Fatalf("alice leaked bob's message: %s", body)
	}

	// Bob (other tenant) sees only Bob Private, never Alice's messages.
	code, body = bob.do(http.MethodGet, "/api/v1/mailbox/folders/"+webmail.EncodeFolderName("INBOX")+"/messages", "")
	var bobList webmail.ListResult
	if code != http.StatusOK || json.Unmarshal([]byte(body), &bobList) != nil || bobList.Total != 1 || bobList.Messages[0].Subject != "Bob Private" {
		t.Fatalf("bob messages = %d %s", code, body)
	}
	if strings.Contains(body, "Alice") {
		t.Fatalf("bob leaked alice's message: %s", body)
	}

	// Same-tenant Carol (tenant 7, mailbox 13) sees 0 messages and cannot read Alice's Archive folder.
	code, body = carol.do(http.MethodGet, "/api/v1/mailbox/messages", "")
	var carolList webmail.ListResult
	if code != http.StatusOK || json.Unmarshal([]byte(body), &carolList) != nil || carolList.Total != 0 || len(carolList.Messages) != 0 {
		t.Fatalf("carol leaked same-tenant messages: %d %s", code, body)
	}
	if code, _ := carol.do(http.MethodGet, "/api/v1/mailbox/folders/Archive/messages", ""); code != http.StatusNotFound {
		t.Fatalf("carol saw alice custom folder Archive: status=%d, want 404", code)
	}

	// Brand boundary rejects cross-brand token replay on folders/messages.
	if code, _ := alice.doHost(http.MethodGet, "/api/v1/mailbox/folders", "", "acme.example.test"); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("cross-brand folder read = %d, want 401/403", code)
	}

	// Direct adapter misuse with mismatched (tenantID, mailboxID) fails closed.
	probe := handlers.SQLiteMailboxStore{DB: db}
	if _, err := probe.ListFolders(ctx, 8, 11); !errors.Is(err, handlers.ErrMailboxNotFound) {
		t.Fatalf("cross-tenant ListFolders err=%v, want ErrMailboxNotFound", err)
	}
	if _, err := probe.ListMessages(ctx, 7, 12, "INBOX", webmail.ListOpts{}); !errors.Is(err, handlers.ErrMailboxNotFound) {
		t.Fatalf("cross-mailbox ListMessages err=%v, want ErrMailboxNotFound", err)
	}
}
