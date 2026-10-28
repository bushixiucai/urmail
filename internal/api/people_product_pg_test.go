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

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestPeopleProductCRUDAndStandardImportPG(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	ctx := t.Context()
	password := "private-people-browser-fixture"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	var brand int64
	if err = platform.QueryRow(ctx, `INSERT INTO brands(slug,name)VALUES('people-product','People')RETURNING id`).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	type account struct {
		tenant, mailbox int64
		address, token  string
	}
	accounts := make([]account, 0, 2)
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	engine := New(Options{PlatformPool: platform, AppPool: app, WebmailNative: true, Messages: messages})
	call := func(token, method, path string, body any, key string) *httptest.ResponseRecorder {
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		request := httptest.NewRequestWithContext(ctx, method, path, bytes.NewReader(raw))
		request.RemoteAddr = "127.0.0.1:21390"
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		result := httptest.NewRecorder()
		engine.ServeHTTP(result, request)
		return result
	}
	decode := func(response *httptest.ResponseRecorder, status int) map[string]any {
		t.Helper()
		if response.Code != status {
			t.Fatalf("HTTP%d want%d: %s", response.Code, status, response.Body.String())
		}
		result := map[string]any{}
		if response.Body.Len() > 0 {
			if e := json.Unmarshal(response.Body.Bytes(), &result); e != nil {
				t.Fatal(e)
			}
		}
		return result
	}
	for i := range 2 {
		a := account{address: fmt.Sprintf("client@people-%d.example.test", i)}
		domain := int64(0)
		if err = platform.QueryRow(ctx, `INSERT INTO tenants(brand_id,name,slug,status)VALUES($1,'People',$2,'active')RETURNING id`, brand, fmt.Sprintf("people-%d", i)).Scan(&a.tenant); err != nil {
			t.Fatal(err)
		}
		if err = platform.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,provisioning_state)VALUES($1,$2,'active','fixture',now(),'ready')RETURNING id`, a.tenant, fmt.Sprintf("people-%d.example.test", i)).Scan(&domain); err != nil {
			t.Fatal(err)
		}
		if err = platform.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes)VALUES($1,$2,'client',$3,$4,104857600)RETURNING id`, a.tenant, domain, a.address, hash).Scan(&a.mailbox); err != nil {
			t.Fatal(err)
		}
		login := decode(call("", "POST", "/api/v1/mailbox/session", map[string]any{"email": a.address, "password": password}, ""), 200)
		a.token = login["token"].(string)
		accounts = append(accounts, a)
	}
	a, b := accounts[0], accounts[1]
	contact := map[string]any{"display_name": "Ada Fixture", "given_name": "Ada", "family_name": "Example", "emails": []map[string]string{{"address": "ada@example.test", "label": "work"}, {"address": "other@example.test", "label": "home"}}, "phones": []map[string]string{{"number": "+1-555-0100", "label": "work"}, {"number": "+86-10-1000", "label": "home"}}, "note": "First line\nSecond line"}
	created := decode(call(a.token, "POST", "/api/v1/mailbox/contacts", contact, ""), 201)
	contactID := int64(created["id"].(float64))
	bookID := int64(created["addressbook_id"].(float64))
	if len(created["emails"].([]any)) != 2 || len(created["phones"].([]any)) != 2 {
		t.Fatal("multi-value contact JSON was lost")
	}
	contact["version"] = 1
	contact["family_name"] = "Updated"
	decode(call(a.token, "PUT", fmt.Sprintf("/api/v1/mailbox/contacts/%d", contactID), contact, ""), 200)
	decode(call(a.token, "PUT", fmt.Sprintf("/api/v1/mailbox/contacts/%d", contactID), contact, ""), 409)
	foreign := call(b.token, "PUT", fmt.Sprintf("/api/v1/mailbox/contacts/%d", contactID), contact, "")
	if foreign.Code < 400 {
		t.Fatal("cross-tenant contact write accepted")
	}
	group := decode(call(a.token, "POST", "/api/v1/mailbox/contacts/groups", map[string]any{"name": "Project"}, ""), 201)
	groupID := int64(group["id"].(float64))
	decode(call(a.token, "POST", "/api/v1/mailbox/contacts/groups/members", map[string]any{"group_id": groupID, "contact_id": contactID, "group_addressbook_id": bookID, "contact_addressbook_id": bookID}, ""), 201)
	members := decode(call(a.token, "GET", fmt.Sprintf("/api/v1/mailbox/contacts/groups/%d/members", groupID), nil, ""), 200)
	if len(members["contacts"].([]any)) != 1 {
		t.Fatal("group membership missing")
	}
	decode(call(b.token, "GET", fmt.Sprintf("/api/v1/mailbox/contacts/groups/%d/members", groupID), nil, ""), 404)
	decode(call(a.token, "DELETE", fmt.Sprintf("/api/v1/mailbox/contacts/groups/%d/members/%d", groupID, contactID), nil, ""), 204)
	exported := call(a.token, "GET", "/api/v1/mailbox/contacts/export.vcf", nil, "")
	if exported.Code != 200 || !strings.Contains(exported.Body.String(), "ada@example.test") || !strings.Contains(exported.Body.String(), "+86-10-1000") {
		t.Fatal("contact standard export lost data")
	}
	source := map[string]any{"kind": "contacts", "format": "csv", "mode": "skip", "content": "name,email\nOne,one@example.test\nBad,not-an-email\nTwo,two@example.test\n"}
	preview := decode(call(a.token, "POST", "/api/v1/mailbox/data/import/preview", source, ""), 200)
	if preview["valid"].(float64) != 2 || len(preview["errors"].([]any)) != 1 {
		t.Fatal("partial import preview incorrect")
	}
	source["digest"] = preview["digest"]
	var count int64
	if err = platform.QueryRow(ctx, `SELECT count(*)FROM mailbox_contacts WHERE tenant_id=$1`, a.tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("preview wrote data count=%d err=%v", count, err)
	}
	receipt := decode(call(a.token, "POST", "/api/v1/mailbox/data/import/commit", source, "people-import-stable-key"), 200)
	if receipt["added"].(float64) != 2 {
		t.Fatal("valid rows not imported")
	}
	decode(call(a.token, "POST", "/api/v1/mailbox/data/import/commit", source, "people-import-stable-key"), 200)
	repeated := decode(call(a.token, "POST", "/api/v1/mailbox/data/import/commit", source, "people-import-another-key"), 200)
	if repeated["added"].(float64) != 0 || repeated["skipped"].(float64) != 2 {
		t.Fatal("skip duplicate policy failed")
	}
	if err = platform.QueryRow(ctx, `SELECT count(*)FROM mailbox_contacts WHERE tenant_id=$1`, a.tenant).Scan(&count); err != nil || count != 3 {
		t.Fatalf("idempotency wrote duplicates count=%d err=%v", count, err)
	}
	series := map[string]any{"summary": "DST series", "starts_at": "2026-03-07T14:00:00Z", "ends_at": "2026-03-07T15:00:00Z", "timezone": "America/New_York", "rrule": "FREQ=DAILY;COUNT=3"}
	event := decode(call(a.token, "POST", "/api/v1/mailbox/calendars/events", series, ""), 201)
	eventID := int64(event["id"].(float64))
	uid := event["uid"].(string)
	path := "/api/v1/mailbox/calendars/events?start=2026-03-07T00:00:00Z&end=2026-03-11T00:00:00Z"
	listed := decode(call(a.token, "GET", path, nil, ""), 200)["events"].([]any)[0].(map[string]any)
	times := listed["occurrences"].([]any)
	if times[0] != "2026-03-07T14:00:00Z" || times[1] != "2026-03-08T13:00:00Z" {
		t.Fatalf("DST wall-clock drift: %v", times)
	}
	update := map[string]any{"version": 1, "summary": "Only one moved", "starts_at": "2026-03-08T16:00:00Z", "ends_at": "2026-03-08T17:00:00Z", "timezone": "America/New_York", "recurrence_id": "2026-03-08T13:00:00Z"}
	decode(call(a.token, "PUT", fmt.Sprintf("/api/v1/mailbox/calendars/events/%d", eventID), update, ""), 200)
	listed = decode(call(a.token, "GET", path, nil, ""), 200)["events"].([]any)[0].(map[string]any)
	if len(listed["recurrence_overrides"].([]any)) != 1 || listed["occurrences"].([]any)[1] != "2026-03-08T16:00:00Z" {
		t.Fatal("single occurrence override not persistent")
	}
	calendar := call(a.token, "GET", "/caldav/calendar/"+uid+".ics", nil, "")
	if calendar.Code != 200 || strings.Count(calendar.Body.String(), "BEGIN:VEVENT") != 2 || !strings.Contains(calendar.Body.String(), "RECURRENCE-ID:20260308T130000Z") {
		t.Fatalf("CalDAV override not standard: %d %s", calendar.Code, calendar.Body.String())
	}
	decode(call(a.token, "DELETE", fmt.Sprintf("/api/v1/mailbox/calendars/events/%d?version=2&recurrence_id=2026-03-09T13:00:00Z", eventID), nil, ""), 200)
	listed = decode(call(a.token, "GET", path, nil, ""), 200)["events"].([]any)[0].(map[string]any)
	if len(listed["occurrences"].([]any)) != 2 {
		t.Fatal("single cancellation removed wrong occurrences")
	}
	calendarImport := map[string]any{"kind": "calendar", "format": "ics", "mode": "skip", "content": calendar.Body.String()}
	calendarPreview := decode(call(b.token, "POST", "/api/v1/mailbox/data/import/preview", calendarImport, ""), 200)
	calendarImport["digest"] = calendarPreview["digest"]
	decode(call(b.token, "POST", "/api/v1/mailbox/data/import/commit", calendarImport, "calendar-roundtrip-key"), 200)
	copied := decode(call(b.token, "GET", path, nil, ""), 200)["events"].([]any)[0].(map[string]any)
	if len(copied["recurrence_overrides"].([]any)) != 1 || copied["timezone"] != "America/New_York" {
		t.Fatal("standard calendar round-trip lost overrides/timezone")
	}
	foreignCalendar := call(b.token, "GET", fmt.Sprintf("/api/v1/mailbox/calendars/events/%d/ics", eventID), nil, "")
	if foreignCalendar.Code < 400 {
		t.Fatal("cross-tenant event export allowed")
	}
	// An exception can move before the master DTSTART; range selection must
	// include it without showing exhausted historical series in later months.
	early := decode(call(a.token, "POST", "/api/v1/mailbox/calendars/events", map[string]any{
		"summary": "Early exception", "starts_at": "2026-05-01T09:00:00Z", "ends_at": "2026-05-01T10:00:00Z", "timezone": "UTC", "rrule": "FREQ=DAILY;COUNT=2",
		"recurrence_overrides": []map[string]any{{"recurrence_id": "2026-05-01T09:00:00Z", "summary": "Moved into April", "starts_at": "2026-04-01T09:00:00Z", "ends_at": "2026-04-01T10:00:00Z", "status": "confirmed"}},
	}, ""), 201)
	april := decode(call(a.token, "GET", "/api/v1/mailbox/calendars/events?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z", nil, ""), 200)["events"].([]any)
	if len(april) != 1 || april[0].(map[string]any)["id"] != early["id"] || len(april[0].(map[string]any)["occurrences"].([]any)) != 1 {
		t.Fatal("range selection lost a moved exception or included exhausted series")
	}
	if directory := os.Getenv("URMAIL_PEOPLE_BROWSER_DIR"); directory != "" {
		server := httptest.NewServer(engine)
		defer server.Close()
		if err = os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		fixture, _ := json.Marshal(map[string]any{"url": server.URL, "email": a.address, "password": password})
		if err = os.WriteFile(filepath.Join(directory, "fixture.json"), fixture, 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("People browser fixture ready")
		deadline := time.Now().Add(8 * time.Minute)
		for time.Now().Before(deadline) {
			if raw, e := os.ReadFile(filepath.Join(directory, "done")); e == nil {
				if strings.TrimSpace(string(raw)) != "PASS" {
					t.Fatal("people browser failed")
				}
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatal("people browser did not finish")
	}
}
