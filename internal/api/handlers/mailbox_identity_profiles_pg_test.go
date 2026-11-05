package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/mail"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/sieve"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type identityFixtureSubmitter struct {
	auth     []string
	messages []webmail.Outgoing
}

func (s *identityFixtureSubmitter) Send(_ context.Context, address, _ string, msg webmail.Outgoing) (webmail.Submission, error) {
	s.auth = append(s.auth, address)
	s.messages = append(s.messages, msg)
	return webmail.Submission{}, nil
}
func seedIdentityFixture(t *testing.T, f nativeAIHTTPFixture) (string, *identityFixtureSubmitter) {
	t.Helper()
	var domain int64
	if err := f.pool.QueryRow(t.Context(), "SELECT domain_id FROM mailboxes WHERE tenant_id=$1 AND id=$2", f.principal.TenantID, f.principal.MailboxID).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE domains SET provisioning_state='ready' WHERE tenant_id=$1 AND id=$2", f.principal.TenantID, domain); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), "INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,kind,active,sender_logins) VALUES($1,$2,'team',$3,'alias',true,ARRAY[$3]::text[])", f.principal.TenantID, domain, f.principal.Address); err != nil {
		t.Fatal(err)
	}
	submit := &identityFixtureSubmitter{}
	f.native.Submitter = submit
	return "team@ai.example.test", submit
}

// identitySendCounter makes each send carry a fresh idempotency key so the
// revoked-identity recheck in this fixture is exercised, not idempotent replay.
var identitySendCounter atomic.Int64

func identityJSON(t *testing.T, f nativeAIHTTPFixture, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	// Sends require an idempotency key; other routes ignore the header.
	req.Header.Set("Idempotency-Key", "identity-profiles-"+strconv.FormatInt(identitySendCounter.Add(1), 10))
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}
func TestMailboxIdentityProfilesAndDraftRevocationPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	alias, submit := seedIdentityFixture(t, f)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	request := map[string]any{"display_name": "Primary Owner", "signature_text": "Primary sign", "signature_html": "<b>Primary sign</b>", "identity_profiles": map[string]any{alias: map[string]string{"display_name": "Team Desk", "signature_text": "Team sign", "signature_html": `<b>Team sign</b><script>alert(1)</script><img src=x onerror="alert(1)">`}}, "preferences": map[string]any{"default_from": alias, "reply_identity": "recipient", "timezone": "America/New_York", "date_format": "iso", "quote_original": false}, "expected_version": 0}
	if rec := identityJSON(t, f, token, "PUT", "/api/v1/mailbox/settings", request); rec.Code != 200 {
		t.Fatalf("settings %d %s", rec.Code, rec.Body.String())
	}
	// A fresh store and session read the same owner-bound identity snapshot.
	persisted, err := (StoreMailboxSelfSettings{Pool: f.pool}).Settings(t.Context(), f.principal.TenantID, f.principal.MailboxID)
	if err != nil {
		t.Fatal(err)
	}
	var preferences map[string]any
	if json.Unmarshal(persisted.Preferences, &preferences) != nil || preferences["quote_original"] != false {
		t.Fatal("quote preference did not persist across store reconstruction")
	}
	if !strings.Contains(string(persisted.IdentityProfiles), "Team Desk") || strings.Contains(string(persisted.IdentityProfiles), "onerror") || strings.Contains(string(persisted.IdentityProfiles), "script") {
		t.Fatalf("unsafe identity HTML persisted: %s", persisted.IdentityProfiles)
	}
	send := map[string]any{"from": alias, "to": []string{"recipient@ai.example.test"}, "subject": "Identity", "text": "Synthetic identity test"}
	if rec := identityJSON(t, f, token, "POST", "/api/v1/mailbox/send", send); rec.Code != 200 {
		t.Fatalf("identity send %d %s", rec.Code, rec.Body.String())
	}
	from, err := mail.ParseAddress(submit.messages[0].From)
	if err != nil || from.Address != alias || from.Name != "Team Desk" || submit.auth[0] != f.principal.Address {
		t.Fatal("trusted display name or SMTP auth identity lost")
	}
	draft := map[string]any{"from": alias, "to": send["to"], "subject": "Alias draft", "text": "Keep my alias"}
	if rec := identityJSON(t, f, token, "PUT", "/api/v1/mailbox/drafts", draft); rec.Code != 200 {
		t.Fatalf("alias draft %d %s", rec.Code, rec.Body.String())
	}
	f.mail.mu.Lock()
	saved := f.mail.messages["Drafts"][f.mail.next]
	f.mail.mu.Unlock()
	if len(saved.From) != 1 || saved.From[0].Email != alias || saved.From[0].Name != "Team Desk" {
		t.Fatal("draft silently changed alias identity")
	}
	// The old UI/draft remains stale; server rechecks the same grant on both paths.
	if _, err := f.pool.Exec(t.Context(), "UPDATE mailbox_aliases SET active=false WHERE tenant_id=$1 AND local_part='team'", f.principal.TenantID); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		method, path string
		body         any
	}{{"POST", "/api/v1/mailbox/send", send}, {"PUT", "/api/v1/mailbox/drafts", draft}} {
		if rec := identityJSON(t, f, token, entry.method, entry.path, entry.body); rec.Code != 403 || !strings.Contains(rec.Body.String(), "MAILBOX_FROM_NOT_AUTHORIZED") {
			t.Fatalf("revoked identity accepted %d %s", rec.Code, rec.Body.String())
		}
	}
	if len(submit.messages) != 1 {
		t.Fatal("revoked identity reached SMTP")
	}
	request["expected_version"] = 1
	request["identity_profiles"] = map[string]any{"other@outside.test": map[string]string{"display_name": "Forged"}}
	if rec := identityJSON(t, f, token, "PUT", "/api/v1/mailbox/settings", request); rec.Code != 403 {
		t.Fatal("arbitrary profile granted an identity")
	}
}

func TestMailboxManagedRuleEditorPersistencePG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	initial := identityJSON(t, f, token, "GET", "/api/v1/mailbox/rules", nil)
	var view struct {
		Version string `json:"version"`
	}
	if initial.Code != 200 || json.Unmarshal(initial.Body.Bytes(), &view) != nil {
		t.Fatal("rule snapshot unavailable")
	}
	now := time.Now().UTC().Truncate(time.Minute)
	desired := sieve.Rules{Rules: []sieve.Rule{{Header: "subject", Match: "contains", Value: "Invoice", Action: "fileinto", Folder: "Finance"}, {Header: "from", Match: "is", Value: "blocked@example.test", Action: "discard", Disabled: true}}, Vacation: sieve.Vacation{Enabled: true, Subject: "Away", Message: "Synthetic automatic reply body", Days: 2, From: now, Until: now.Add(24 * time.Hour)}}
	request := map[string]any{"confirmed": false, "expected_version": view.Version, "rules": desired}
	if rec := identityJSON(t, f, token, "PUT", "/api/v1/mailbox/rules", request); rec.Code != 400 {
		t.Fatal("unconfirmed rule edits activated")
	}
	request["confirmed"] = true
	if rec := identityJSON(t, f, token, "PUT", "/api/v1/mailbox/rules", request); rec.Code != 200 {
		t.Fatalf("valid editor payload %d %s", rec.Code, rec.Body.String())
	}
	script, mode, err := (StoreMailboxRules{Pool: f.pool}).Sieve(t.Context(), f.principal.TenantID, f.principal.MailboxID)
	if err != nil || mode != "managed" {
		t.Fatal("persisted rule mode lost")
	}
	restored, err := sieve.Decode(script)
	if err != nil || len(restored.Rules) != 2 || !restored.Rules[1].Disabled || restored.Vacation.Message != desired.Vacation.Message || !restored.Vacation.From.Equal(now) || strings.Count(script, "if header") != 1 {
		t.Fatalf("editor snapshot failed canonical roundtrip: %+v %v", restored, err)
	}
	// A stale edit must not replace another version or silently activate.
	if rec := identityJSON(t, f, token, "PUT", "/api/v1/mailbox/rules", request); rec.Code != 409 {
		t.Fatal("stale rule version overwritten")
	}
	f.native.Sieve = nil
	request["expected_version"] = sieve.Digest(script)
	if rec := identityJSON(t, f, token, "PUT", "/api/v1/mailbox/rules", request); rec.Code != 503 {
		t.Fatal("missing activator falsely reported active")
	}
}
