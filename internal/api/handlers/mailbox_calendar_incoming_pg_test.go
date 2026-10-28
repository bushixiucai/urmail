package handlers

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"
	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type schedulingFixtureMail struct {
	*nativeAIFixtureMail
	raw map[uint32][]byte
}

func (f *schedulingFixtureMail) GetRawMessage(_ context.Context, folder string, uid uint32) ([]byte, uint32, error) {
	if folder != "INBOX" || f.raw[uid] == nil {
		return nil, 9, webmail.ErrMessageNotFound
	}
	return append([]byte(nil), f.raw[uid]...), 9, nil
}

func (f *schedulingFixtureMail) SaveSentCopy(_ context.Context, raw []byte) (webmail.DraftSaveResult, error) {
	if len(raw) == 0 {
		return webmail.DraftSaveResult{}, webmail.ErrSentCopyPending
	}
	return webmail.DraftSaveResult{Folder: "Sent", UID: 1, UIDValidity: 9}, nil
}

type schedulingFixtureDialer struct{ client webmail.Client }

func (f schedulingFixtureDialer) Dial(context.Context, string, string) (webmail.Client, error) {
	return f.client, nil
}

type schedulingHTTPFixture struct {
	base    nativeAIHTTPFixture
	mail    *schedulingFixtureMail
	handler MailboxCalendarHandler
	token   string
	key     ed25519.PrivateKey
	submit  *fakeInvitationSubmitter
}

func newSchedulingHTTPFixture(t *testing.T) *schedulingHTTPFixture {
	t.Helper()
	base := newNativeAIHTTPFixture(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &schedulingHTTPFixture{base: base, mail: &schedulingFixtureMail{nativeAIFixtureMail: base.mail, raw: map[uint32][]byte{}}, key: private, submit: &fakeInvitationSubmitter{}}
	base.native.Dialer = schedulingFixtureDialer{client: f.mail}
	f.handler = MailboxCalendarHandler{Pool: base.pool, Service: appcollab.Service{Q: store.New(base.pool)}, Native: base.native, Submitter: f.submit, Vault: base.native.Vault, SchedulingTXT: func(_ context.Context, name string) ([]string, error) {
		if name != "test._domainkey.outside.example" {
			return nil, fmt.Errorf("unknown isolated selector")
		}
		return []string{"v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(public)}, nil
	}}
	f.token = loginNative(t, base.router, base.principal.Address, base.password)
	return f
}
func (f *schedulingHTTPFixture) sign(t *testing.T, from, to, body string, sequence int) []byte {
	t.Helper()
	raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nMessage-ID: <invite-%d@outside.example>\r\nMIME-Version: 1.0\r\nContent-Type: text/calendar; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s", from, to, sequence, body)
	var out bytes.Buffer
	if err := dkim.Sign(&out, strings.NewReader(raw), &dkim.SignOptions{Domain: "outside.example", Selector: "test", Signer: f.key, HeaderKeys: []string{"From", "To", "Message-ID", "MIME-Version", "Content-Type", "Content-Transfer-Encoding"}, HeaderCanonicalization: dkim.CanonicalizationRelaxed, BodyCanonicalization: dkim.CanonicalizationRelaxed}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func (f *schedulingHTTPFixture) request(t *testing.T, method string, uid uint32, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	group := r.Group("/api/v1/mailbox", middleware.RequireMailboxAuth(StoreMailboxNative{Q: store.New(f.base.pool)}, f.base.native.Vault))
	RegisterCalendarIncomingRoutes(group, f.handler)
	path := "/api/v1/mailbox/calendars/invitations"
	if method == "GET" {
		path += "?" + url.Values{"folder_enc": {webmail.EncodeFolderName("INBOX")}, "uid": {fmt.Sprint(uid)}, "uid_validity": {"9"}}.Encode()
	}
	var text []byte
	if body != nil {
		var err error
		text, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(text))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}
func (f *schedulingHTTPFixture) preview(t *testing.T, uid uint32) map[string]any {
	t.Helper()
	rec := f.request(t, "GET", uid, nil)
	if rec.Code != 200 {
		t.Fatalf("preview %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatal("invalid preview")
	}
	return out
}
func (f *schedulingHTTPFixture) action(t *testing.T, uid uint32, preview map[string]any, action string) *httptest.ResponseRecorder {
	t.Helper()
	version := float64(0)
	if linked, ok := preview["linked_event"].(map[string]any); ok {
		version = linked["version"].(float64)
	}
	return f.request(t, "POST", uid, map[string]any{"source": map[string]any{"folder_enc": webmail.EncodeFolderName("INBOX"), "uid": uid, "uid_validity": 9}, "source_digest": preview["source_digest"], "action": action, "expected_version": version, "confirmed": true})
}

func TestIncomingInvitationDKIMSourceResponseUpdateCancelPG(t *testing.T) {
	f := newSchedulingHTTPFixture(t)
	start := time.Now().UTC().AddDate(0, 0, 2).Truncate(time.Second)
	stamp := time.Now().UTC().Truncate(time.Second)
	event := collab.Event{UID: "external-series@outside.example", Summary: "Verified external meeting", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: "UTC", RRule: "FREQ=WEEKLY;COUNT=3", Attendees: []string{f.base.principal.Address}}
	invite := collab.Invitation{UID: event.UID, Sequence: 0, Method: collab.MethodRequest, Organizer: "host@outside.example", Stamp: stamp, Event: event}
	body, err := collab.InvitationICS(invite)
	if err != nil {
		t.Fatal(err)
	}
	f.mail.raw[10] = f.sign(t, invite.Organizer, f.base.principal.Address, body, 0)
	preview := f.preview(t, 10)
	if preview["verification"] != "verified" {
		t.Fatalf("real DKIM signature not verified %+v", preview)
	}
	rec := f.action(t, 10, preview, "accept")
	if rec.Code != 200 || len(f.submit.calls) != 1 {
		t.Fatalf("response %d %s", rec.Code, rec.Body.String())
	}
	response := string(f.submit.calls[0].Attachments[0].Content)
	if !strings.Contains(response, "METHOD:REPLY") || !strings.Contains(response, "PARTSTAT=ACCEPTED") || !strings.Contains(response, "SEQUENCE:0") {
		t.Fatal("response did not retain organizer sequence")
	}
	if rec = f.action(t, 10, preview, "accept"); rec.Code != 200 || len(f.submit.calls) != 1 {
		t.Fatal("fresh handler repeated response")
	}
	saved, err := store.New(f.base.pool).PlatformFindSchedulingEvent(t.Context(), store.PlatformFindSchedulingEventParams{TenantID: f.base.principal.TenantID, MailboxID: f.base.principal.MailboxID, Uid: event.UID})
	if err != nil || saved.Organizer != invite.Organizer || saved.InvitationResponse != "accepted" {
		t.Fatalf("organizer association lost %+v %v", saved, err)
	}
	invite.Sequence = 1
	invite.Stamp = stamp.Add(time.Second)
	invite.Event.StartsAt = start.Add(2 * time.Hour)
	invite.Event.EndsAt = start.Add(3 * time.Hour)
	body, err = collab.InvitationICS(invite)
	if err != nil {
		t.Fatal(err)
	}
	f.mail.raw[11] = f.sign(t, invite.Organizer, f.base.principal.Address, body, 1)
	if rec = f.action(t, 11, f.preview(t, 11), "apply_update"); rec.Code != 200 {
		t.Fatalf("update %d %s", rec.Code, rec.Body.String())
	}
	old := f.preview(t, 10)
	if rec = f.action(t, 10, old, "apply_update"); rec.Code != 403 {
		t.Fatalf("old sequence overwritten %d %s", rec.Code, rec.Body.String())
	}
	recurrence := invite.Event.StartsAt.AddDate(0, 0, 7)
	//nolint:misspell // RFC 5545 CANCELLED is a protocol value.
	cancel := fmt.Sprintf("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\nUID:%s\r\nORGANIZER:mailto:%s\r\nSEQUENCE:2\r\nDTSTAMP:%s\r\nRECURRENCE-ID:%s\r\nSTATUS:CANCELLED\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n", event.UID, invite.Organizer, stamp.Add(2*time.Second).Format("20060102T150405Z"), recurrence.Format("20060102T150405Z"))
	f.mail.raw[12] = f.sign(t, invite.Organizer, f.base.principal.Address, cancel, 2)
	if rec = f.action(t, 12, f.preview(t, 12), "apply_cancel"); rec.Code != 200 {
		t.Fatalf("single cancellation %d %s", rec.Code, rec.Body.String())
	}
	saved, err = store.New(f.base.pool).PlatformFindSchedulingEvent(t.Context(), store.PlatformFindSchedulingEventParams{TenantID: f.base.principal.TenantID, MailboxID: f.base.principal.MailboxID, Uid: event.UID})
	if err != nil {
		t.Fatal(err)
	}
	actual, _, err := appcollab.EventFromRow(saved)
	if err != nil || actual.Status == "cancelled" || len(actual.Overrides) != 1 || actual.Overrides[0].Status != "cancelled" { //nolint:misspell // RFC 5545 value.
		t.Fatalf("cancel changed entire series %+v %v", actual, err)
	} //nolint:misspell // RFC 5545 value.
}

func TestIncomingInvitationUnverifiedCopyAndUnknownHoldPG(t *testing.T) {
	f := newSchedulingHTTPFixture(t)
	start := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	invite := collab.Invitation{UID: "unknown@outside.example", Method: collab.MethodRequest, Organizer: "host@outside.example", Stamp: start.Add(-time.Hour), Event: collab.Event{Summary: "Original", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: "UTC", Attendees: []string{f.base.principal.Address}}}
	body, err := collab.InvitationICS(invite)
	if err != nil {
		t.Fatal(err)
	}
	signed := f.sign(t, invite.Organizer, f.base.principal.Address, body, 0)
	f.mail.raw[10] = bytes.Replace(signed, []byte("SUMMARY:Original"), []byte("SUMMARY:Tampered"), 1)
	preview := f.preview(t, 10)
	if preview["verification"] != "unverified" {
		t.Fatal("modified body was trusted")
	}
	if rec := f.action(t, 10, preview, "accept"); rec.Code != 403 || len(f.submit.calls) != 0 {
		t.Fatal("unverified organizer response sent")
	}
	if rec := f.action(t, 10, preview, "copy_personal"); rec.Code != 200 {
		t.Fatalf("personal copy %d %s", rec.Code, rec.Body.String())
	}
	var owner string
	if err = f.base.pool.QueryRow(t.Context(), "SELECT organizer FROM mailbox_events WHERE tenant_id=$1 AND mailbox_id=$2 AND summary='Tampered'", f.base.principal.TenantID, f.base.principal.MailboxID).Scan(&owner); err != nil || owner != "" {
		t.Fatal("personal copy inherited organizer authority")
	}
	f.mail.raw[11] = signed
	f.submit.err = webmail.ErrSubmitUncertain
	preview = f.preview(t, 11)
	if rec := f.action(t, 11, preview, "tentative"); rec.Code != 409 {
		t.Fatalf("lost SMTP result %d %s", rec.Code, rec.Body.String())
	}
	calls := len(f.submit.calls)
	f.submit.err = nil
	if rec := f.action(t, 11, preview, "tentative"); rec.Code != 409 || len(f.submit.calls) != calls {
		t.Fatal("unknown response resent")
	}
	fresh := f.preview(t, 11)
	if fresh["notification_status"] != "uncertain" || len(fresh["actions"].([]any)) != 0 {
		t.Fatal("unknown state was not visible after restart")
	}
	f.mail.raw[12] = f.sign(t, invite.Organizer, "other@example.test", body, 2)
	if rec := f.request(t, "GET", 12, nil); rec.Code != 403 {
		t.Fatal("another recipient invitation was accessible")
	}
}

func TestIncomingAttendeeReplyUsesOriginalMessageAndSequencePG(t *testing.T) {
	f := newSchedulingHTTPFixture(t)
	p := appcollab.Principal{TenantID: f.base.principal.TenantID, MailboxID: f.base.principal.MailboxID}
	svc := f.handler.Service
	calendar, err := svc.EnsureCalendar(t.Context(), p, defaultCalendarName, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	event := collab.Event{UID: "owned@urmail", Summary: "Owned meeting", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: "UTC", Attendees: []string{"host@outside.example"}}
	saved, err := svc.CreateEvent(t.Context(), p, calendar.ID, event.UID, event)
	if err != nil {
		t.Fatal(err)
	}
	if n, e := store.New(f.base.pool).PlatformBindOwnedCalendarOrganizer(t.Context(), store.PlatformBindOwnedCalendarOrganizerParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: saved.ID}); e != nil || n != 1 {
		t.Fatal("own organizer was not bound")
	}
	body, err := collab.ResponseICS(event, f.base.principal.Address, "host@outside.example", collab.PartStatAccepted, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.mail.raw[10] = f.sign(t, "host@outside.example", f.base.principal.Address, body, 0)
	if rec := f.action(t, 10, f.preview(t, 10), "apply_reply"); rec.Code != 200 {
		t.Fatalf("reply %d %s", rec.Code, rec.Body.String())
	}
	row, err := store.New(f.base.pool).PlatformGetEvent(t.Context(), store.PlatformGetEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: saved.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, status := appcollab.DecodeAttendees(row.Attendees)
	if status["host@outside.example"] != collab.PartStatAccepted || row.Sequence != 0 || row.Version != 2 {
		t.Fatalf("reply changed organizer sequence or wrong attendee %+v", row)
	}
	// A trusted signing domain cannot authorize a body claiming another sender.
	forged := strings.Replace(body, "mailto:host@outside.example", "mailto:someone@outside.example", 1)
	f.mail.raw[11] = f.sign(t, "host@outside.example", f.base.principal.Address, forged, 1)
	if rec := f.request(t, "GET", 11, nil); rec.Code == 200 {
		t.Fatal("signed From/body attendee mismatch trusted")
	}
}
