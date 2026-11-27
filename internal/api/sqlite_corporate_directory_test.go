package api_test

// b6 structural unlock: the mailbox corporate directory is functionally
// served by the SQLite slice store with application-layer tenant scoping
// (architecture ruling ① — every query binds the session's TenantID, no
// RLS). These tests pin the functional contract and the tenant isolation
// both ways: alice must never see tenant 8 rows (including an email-field
// hit planted on tenant 8), bob never tenant 7 rows, and the LIKE wildcards
// in a search term must match literally.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestCorporateDirectorySliceIsolation(t *testing.T) {
	db, server := newSlice1Server(t)
	ctx := context.Background()
	const password = "slice1-mailbox-password"

	for _, stmt := range []string{
		// Directory enabled for tenants 7 (alice) and 8 (bob).
		`INSERT INTO tenant_directory_settings(tenant_id,enabled,updated_at) VALUES(7,1,1737412800000000),(8,1,1737412800000000)`,
		`INSERT INTO tenant_directory_contacts(tenant_id,display_name,department,job_title,emails,phones,note) VALUES
			(7,'Alice Chen','Engineering','Staff', '["alice.chen@a.example.test"]','[]',''),
			(7,'Alicia Ops','Operations','Lead','["ops@a.example.test"]','[]',''),
			(8,'Bob Smith','Support','Agent','["bob@b.example.test"]','[]',''),
			(8,'Remote Colleague','Field','Agent','["alice-mirror@other.example"]','[]','')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	login := func(email string) *slice1Client {
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
	alice := login("alice@a.example.test")
	bob := login("bob@b.example.test")

	// alice List: exactly the two tenant-7 contacts, enabled, never tenant 8.
	code, body := alice.do(http.MethodGet, "/api/v1/mailbox/corporate-directory", "")
	if code != http.StatusOK {
		t.Fatalf("alice list = %d %s", code, body)
	}
	var list struct {
		Enabled  bool `json:"enabled"`
		Contacts []struct {
			DisplayName string `json:"display_name"`
		} `json:"contacts"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("alice list decode: %v %s", err, body)
	}
	if !list.Enabled || len(list.Contacts) != 2 {
		t.Fatalf("alice list enabled=%v contacts=%d, want enabled + 2 tenant-7 rows (%s)", list.Enabled, len(list.Contacts), body)
	}
	if contains(body, "Bob Smith") || contains(body, "Remote Colleague") {
		t.Fatalf("alice list leaked tenant-8 rows: %s", body)
	}

	// alice Search by name: tenant-7 rows only. The tenant-8 "Remote
	// Colleague" contact carries an alice-shaped EMAIL on purpose: an email
	// hit there would prove a tenant-scope leak.
	code, body = alice.do(http.MethodGet, "/api/v1/mailbox/corporate-directory/search?q=alice", "")
	if code != http.StatusOK {
		t.Fatalf("alice search = %d %s", code, body)
	}
	if !contains(body, "Alice Chen") || contains(body, "Remote Colleague") || contains(body, "alice-mirror") {
		t.Fatalf("alice search crossed the tenant scope: %s", body)
	}

	// bob Search: his tenant's email-field hit works (the search really
	// spans emails) and no tenant-7 row appears.
	code, body = bob.do(http.MethodGet, "/api/v1/mailbox/corporate-directory/search?q=alice-mirror", "")
	if code != http.StatusOK || !contains(body, "Remote Colleague") {
		t.Fatalf("bob email search = %d %s, want the tenant-8 contact", code, body)
	}
	if contains(body, "Alice Chen") {
		t.Fatalf("bob search leaked tenant-7 rows: %s", body)
	}

	// LIKE wildcards in a search term match literally, not as wildcards.
	code, body = alice.do(http.MethodGet, "/api/v1/mailbox/corporate-directory/search?q=100%25", "")
	if code != http.StatusOK || !contains(body, `"contacts":[]`) {
		t.Fatalf("alice wildcard-escape search = %d %s, want empty contacts", code, body)
	}

	// Disabled directory: an empty, disabled payload, not an error.
	if _, err := db.ExecContext(ctx, `UPDATE tenant_directory_settings SET enabled=0 WHERE tenant_id=7`); err != nil {
		t.Fatal(err)
	}
	code, body = alice.do(http.MethodGet, "/api/v1/mailbox/corporate-directory", "")
	if code != http.StatusOK || !contains(body, `"enabled":false`) || !contains(body, `"contacts":[]`) {
		t.Fatalf("alice disabled list = %d %s, want empty disabled payload", code, body)
	}
}
