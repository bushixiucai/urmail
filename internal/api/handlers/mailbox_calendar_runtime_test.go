package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// TestCalendarInvitationRuntime uses actual Postfix/OpenDKIM/Dovecot, PG and
// HTTP. Only DNS TXT retrieval is controlled to publish the real isolated MTA
// key without changing public DNS. No mail transport or signature is mocked.
func TestCalendarInvitationRuntime(t *testing.T) {
	if os.Getenv("URMAIL_WEBMAIL_RUNTIME") != "1" {
		t.Skip("requires isolated native mailstack harness")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	p, _ := testdb.OpenMigrated(t)
	password := "isolated-fixture-password"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	var tenant, domain int64
	if err = p.QueryRow(ctx, `WITH b AS(INSERT INTO brands(slug,name) VALUES('calendar-runtime','Calendar runtime') RETURNING id),t AS(INSERT INTO tenants(brand_id,name,slug,status) SELECT id,'Calendar runtime','calendar-runtime','active' FROM b RETURNING id) SELECT id FROM t`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'example.test','active','fixture',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	mailbox := map[string]int64{}
	for _, name := range []string{"alice", "bob"} {
		var id int64
		if err = p.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,$3,$4,$5,104857600) RETURNING id", tenant, domain, name, name+"@example.test", hash).Scan(&id); err != nil {
			t.Fatal(err)
		}
		mailbox[name] = id
	}
	publicPEM, err := os.ReadFile("/tmp/native-mail-runtime/dkim-public.pem")
	if err != nil {
		t.Fatal(err)
	}
	public, _ := pem.Decode(publicPEM)
	if public == nil {
		t.Fatal("runtime MTA public key not readable")
	}
	q := store.New(p)
	auth := StoreMailboxNative{Q: q}
	dialer := webmail.NetDialer{Addr: "127.0.0.1:993", Timeout: 5 * time.Second}
	submitter := webmail.NetSubmitter{Addr: "127.0.0.1:587", Timeout: 5 * time.Second}
	native := &MailboxNativeHandler{Enabled: true, Auth: auth, Sessions: auth, Vault: webmail.NewMemoryVault(), Dialer: dialer, Submitter: submitter, Pool: p, SelfSettings: StoreMailboxSelfSettings{Pool: p}}
	handler := MailboxCalendarHandler{Pool: p, Service: appcollab.Service{Q: q}, Native: native, Submitter: submitter, Vault: native.Vault, SchedulingTXT: func(_ context.Context, name string) ([]string, error) {
		if name != "runtime._domainkey.example.test" {
			return nil, fmt.Errorf("unpublished isolated selector")
		}
		return []string{"v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(public.Bytes)}, nil
	}}
	router := gin.New()
	router.POST("/api/v1/mailbox/session", native.CreateSession)
	group := router.Group("/api/v1/mailbox", middleware.RequireMailboxAuth(auth, native.Vault))
	RegisterCalendarIncomingRoutes(group, handler)
	group.POST("/calendars/events/:id/invitations", handler.SendInvitations)
	group.DELETE("/calendars/events/:id", handler.DeleteEvent)
	server := httptest.NewServer(router)
	defer server.Close()
	request := func(user, method, path string, body any) (int, []byte) {
		t.Helper()
		var encoded []byte
		if body != nil {
			var e error
			encoded, e = json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
		}
		req, e := http.NewRequestWithContext(ctx, method, server.URL+"/api/v1/mailbox"+path, bytes.NewReader(encoded))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		if user != "" {
			req.Header.Set("Authorization", "Bearer "+user)
		}
		response, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = response.Body.Close() }()
		raw, e := io.ReadAll(response.Body)
		if e != nil {
			t.Fatal(e)
		}
		return response.StatusCode, raw
	}
	tokens := map[string]string{}
	for _, name := range []string{"alice", "bob"} {
		status, raw := request("", "POST", "/session", map[string]string{"email": name + "@example.test", "password": password})
		var session struct {
			Token string `json:"token"`
		}
		if status != 200 || json.Unmarshal(raw, &session) != nil || session.Token == "" {
			t.Fatalf("real mailbox login %s status=%d", name, status)
		}
		tokens[name] = session.Token
	}
	start := time.Now().UTC().AddDate(0, 0, 2).Truncate(time.Second)
	owner := appcollab.Principal{TenantID: tenant, MailboxID: mailbox["bob"]}
	calendar, err := handler.Service.EnsureCalendar(ctx, owner, "Calendar", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	event := collab.Event{Summary: fmt.Sprintf("Real invitation %d", time.Now().UnixNano()), StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: "UTC", RRule: "FREQ=WEEKLY;COUNT=3", Attendees: []string{"alice@example.test"}}
	saved, err := handler.Service.CreateEvent(ctx, owner, calendar.ID, "", event)
	if err != nil {
		t.Fatal(err)
	}
	waitMessage := func(address, subject string, after ...uint32) calendarMailSource {
		t.Helper()
		for range 80 {
			client, e := dialer.Dial(ctx, address, password)
			if e != nil {
				t.Fatal(e)
			}
			listed, e := client.ListMessages(ctx, "INBOX", webmail.ListOpts{Query: `subject:"` + subject + `"`, Limit: 100})
			if e == nil {
				for _, message := range listed.Messages {
					if message.Subject == subject && (len(after) == 0 || message.UID > after[0]) {
						validity, e := client.FolderUIDValidity(ctx, "INBOX")
						_ = client.Close()
						if e != nil {
							t.Fatal(e)
						}
						return calendarMailSource{FolderEnc: webmail.EncodeFolderName("INBOX"), UID: message.UID, UIDValidity: validity}
					}
				}
			}
			_ = client.Close()
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
		t.Fatalf("real message not delivered %s", subject)
		return calendarMailSource{}
	}
	preview := func(token string, source calendarMailSource) map[string]any {
		t.Helper()
		path := "/calendars/invitations?" + url.Values{"folder_enc": {source.FolderEnc}, "uid": {fmt.Sprint(source.UID)}, "uid_validity": {fmt.Sprint(source.UIDValidity)}}.Encode()
		status, raw := request(token, "GET", path, nil)
		var data map[string]any
		if status != 200 || json.Unmarshal(raw, &data) != nil {
			t.Fatalf("real invitation preview status=%d %s", status, raw)
		}
		if data["verification"] != "verified" {
			t.Fatalf("OpenDKIM signature not verified: %s", raw)
		}
		return data
	}
	action := func(token string, source calendarMailSource, data map[string]any, verb string) {
		t.Helper()
		version := float64(0)
		if linked, ok := data["linked_event"].(map[string]any); ok {
			version = linked["version"].(float64)
		}
		status, raw := request(token, "POST", "/calendars/invitations", map[string]any{"source": source, "source_digest": data["source_digest"], "action": verb, "expected_version": version, "confirmed": true})
		if status != 200 {
			t.Fatalf("real invitation action %s: %d %s", verb, status, raw)
		}
	}
	path := fmt.Sprintf("/calendars/events/%d", saved.ID)
	if status, raw := request(tokens["bob"], "POST", path+"/invitations", map[string]bool{"confirm": true}); status != 202 {
		t.Fatalf("real REQUEST submit %d %s", status, raw)
	}
	incoming := waitMessage("alice@example.test", "Invitation: "+event.Summary)
	data := preview(tokens["alice"], incoming)
	action(tokens["alice"], incoming, data, "accept")
	action(tokens["alice"], incoming, data, "accept")
	reply := waitMessage("bob@example.test", "Re: "+event.Summary)
	action(tokens["bob"], reply, preview(tokens["bob"], reply), "apply_reply")
	ownerRow, err := q.PlatformGetEvent(ctx, store.PlatformGetEventParams{TenantID: tenant, MailboxID: mailbox["bob"], ID: saved.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, statuses := appcollab.DecodeAttendees(ownerRow.Attendees)
	if statuses["alice@example.test"] != "accepted" || ownerRow.Sequence != 0 {
		t.Fatal("real organizer did not receive attendee response")
	}
	event.StartsAt = start.Add(time.Hour)
	event.EndsAt = start.Add(2 * time.Hour)
	if _, err = handler.Service.UpdateEvent(ctx, owner, saved.ID, ownerRow.Version, event); err != nil {
		t.Fatal(err)
	}
	if status, raw := request(tokens["bob"], "POST", path+"/invitations", map[string]bool{"confirm": true}); status != 202 {
		t.Fatalf("real update submit %d %s", status, raw)
	}
	// Search returns newest first: the sequence check proves this is the update,
	// rather than accidentally reusing the original UID from the same subject.
	updated := waitMessage("alice@example.test", "Invitation: "+event.Summary, incoming.UID)
	updateData := preview(tokens["alice"], updated)
	if updateData["sequence"].(float64) != 1 {
		t.Fatal("real updated sequence not delivered")
	}
	action(tokens["alice"], updated, updateData, "apply_update")
	if status, raw := request(tokens["bob"], "DELETE", path+fmt.Sprintf("?version=%d&notify=attendees", ownerRow.Version+1), nil); status != 200 {
		t.Fatalf("real cancel submit %d %s", status, raw)
	}
	canceled := waitMessage("alice@example.test", "Cancelled: "+event.Summary) //nolint:misspell // Matches the actual cancellation subject.
	cancelData := preview(tokens["alice"], canceled)
	if cancelData["sequence"].(float64) != 2 {
		t.Fatal("real cancellation sequence incorrect")
	}
	action(tokens["alice"], canceled, cancelData, "apply_cancel")
	var state string
	if err = p.QueryRow(ctx, "SELECT status FROM mailbox_events WHERE tenant_id=$1 AND mailbox_id=$2 AND uid=$3", tenant, mailbox["alice"], saved.Uid).Scan(&state); err != nil || !strings.EqualFold(state, "cancelled") { //nolint:misspell // RFC 5545 value.
		t.Fatalf("real cancellation not stored %s %v", state, err)
	} //nolint:misspell // RFC 5545 value.
	t.Log("real OpenDKIM/Postfix/Dovecot REQUEST/REPLY/update/CANCEL, original-source validation, PG ownership and response dedupe passed")
}
