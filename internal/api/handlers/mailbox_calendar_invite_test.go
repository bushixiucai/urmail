package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type fakeInvitationSubmitter struct {
	mu    sync.Mutex
	calls []webmail.Outgoing
	err   error
}

func (f *fakeInvitationSubmitter) Send(_ context.Context, address, _ string, msg webmail.Outgoing) (webmail.Submission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return webmail.Submission{}, f.err
	}
	if msg.From != address {
		return webmail.Submission{}, webmail.ErrSubmitFailed
	}
	f.calls = append(f.calls, msg)
	return webmail.Submission{Raw: []byte("raw")}, nil
}

func invitationHandler(t *testing.T, st *fakeCalendarStore, submitter webmail.Submitter) (*gin.Engine, MailboxCalendarHandler, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	sender := collab.InviteSender(nil)
	if submitter != nil {
		sender = SendInvitationsAs(submitter)
	}
	h := MailboxCalendarHandler{Service: appcollab.Service{Q: st}, InviteSender: sender}
	h.invitationJournal = func(ctx context.Context, p appcollab.Principal, row store.MailboxEvents, method string, cancelEvent bool) (func(context.Context) error, error) {
		if !defaultInvitationGate.allow(row.Uid, int64(row.Sequence), method) {
			return nil, errInvitationAlreadySent
		}
		if cancelEvent {
			if _, err := st.PlatformDeleteEvent(ctx, store.PlatformDeleteEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: row.ID, Version: row.Version}); err != nil {
				return nil, err
			}
		}
		return func(context.Context) error { return nil }, nil
	}

	r := gin.New()
	r.POST("/api/v1/mailbox/calendars/events/:id/invitations", func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: 7, MailboxID: 9, Address: "organizer@example.test"})
		c.Set(middleware.ContextMailboxTokenHash, "token")
		h.SendInvitations(c)
	})
	r.PUT("/api/v1/mailbox/calendars/events/:id", func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: 7, MailboxID: 9, Address: "organizer@example.test"})
		c.Set(middleware.ContextMailboxTokenHash, "token")
		h.UpdateEvent(c)
	})
	r.DELETE("/api/v1/mailbox/calendars/events/:id", func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: 7, MailboxID: 9, Address: "organizer@example.test"})
		c.Set(middleware.ContextMailboxTokenHash, "token")
		h.DeleteEvent(c)
	})
	r.POST("/api/v1/mailbox/calendars/events/:id/reply", func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: 7, MailboxID: 9, Address: "organizer@example.test"})
		h.IngestReply(c)
	})
	return r, h, "organizer@example.test"
}

func seedInvitedEvent(t *testing.T, st *fakeCalendarStore) store.MailboxEvents {
	t.Helper()
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	attendees, _ := json.Marshal([]string{"ada@example.test", "bob@example.test"})
	row := store.MailboxEvents{
		ID: 42, TenantID: 7, MailboxID: 9, Uid: "uid-42@urmail", Version: 1, Sequence: 0,
		Summary: "Standup", StartsAt: timestamptz(start), EndsAt: timestamptz(start.Add(time.Hour)),
		Timezone: "UTC", Status: "confirmed", Attendees: attendees,
	}
	st.put(row)
	return row
}

func invitationRequest(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestInvitationHTTPRequiresConfirmation(t *testing.T) {
	store := &fakeCalendarStore{}
	seedInvitedEvent(t, store)
	submit := &fakeInvitationSubmitter{}
	r, _, _ := invitationHandler(t, store, submit)
	w := invitationRequest(r, http.MethodPost, "/api/v1/mailbox/calendars/events/42/invitations",
		`{"confirm":false}`)
	if w.Code != http.StatusBadRequest || len(submit.calls) != 0 {
		t.Fatalf("status=%d sends=%d body=%s", w.Code, len(submit.calls), w.Body.String())
	}
	// A nil submission path refuses even with confirmation: without a
	// wired sender the endpoint is unavailable, never partially sent.
	r2, _, _ := invitationHandler(t, store, nil)
	w2 := invitationRequest(r2, http.MethodPost, "/api/v1/mailbox/calendars/events/42/invitations",
		`{"confirm":true}`)
	if w2.Code != http.StatusServiceUnavailable || !strings.Contains(w2.Body.String(), "WEBMAIL_DISABLED") || len(submit.calls) != 0 {
		t.Fatalf("nil sender status=%d sends=%d body=%s", w2.Code, len(submit.calls), w2.Body.String())
	}
}

func TestInvitationHTTPSendsExactlyOnePerAttendee(t *testing.T) {
	submit := &fakeInvitationSubmitter{}
	// reset the process-wide gate between tests
	*defaultInvitationGate = *newInvitationGate()
	store := &fakeCalendarStore{}
	seedInvitedEvent(t, store)
	r, _, organizer := invitationHandler(t, store, submit)
	w := invitationRequest(r, http.MethodPost, "/api/v1/mailbox/calendars/events/42/invitations",
		`{"confirm":true}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(submit.calls) != 2 {
		t.Fatalf("sends=%d", len(submit.calls))
	}
	seen := map[string]bool{}
	for _, msg := range submit.calls {
		if len(msg.To) != 1 {
			t.Fatalf("one recipient per message, got %v", msg.To)
		}
		seen[msg.To[0]] = true
		if msg.From != organizer {
			t.Fatalf("from=%s", msg.From)
		}
		ics := string(msg.Attachments[0].Content)
		if !strings.Contains(msg.Attachments[0].ContentType, "method=REQUEST") {
			t.Fatalf("content type %q", msg.Attachments[0].ContentType)
		}
		for _, want := range []string{"METHOD:REQUEST", "UID:uid-42@urmail", "SEQUENCE:0", "ORGANIZER:mailto:organizer@example.test", "ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:ada@example.test"} {
			if !strings.Contains(ics, want) {
				t.Fatalf("missing %q in %q", want, ics)
			}
		}
	}
	if !seen["ada@example.test"] || !seen["bob@example.test"] {
		t.Fatalf("recipients %v", seen)
	}
}

func TestInvitationHTTPRateLimitedPerVersion(t *testing.T) {
	*defaultInvitationGate = *newInvitationGate()
	submit := &fakeInvitationSubmitter{}
	store := &fakeCalendarStore{}
	seedInvitedEvent(t, store)
	r, _, _ := invitationHandler(t, store, submit)
	body := `{"confirm":true}`
	if w := invitationRequest(r, http.MethodPost, "/api/v1/mailbox/calendars/events/42/invitations", body); w.Code != http.StatusAccepted {
		t.Fatalf("first status=%d", w.Code)
	}
	w := invitationRequest(r, http.MethodPost, "/api/v1/mailbox/calendars/events/42/invitations", body)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "INVITATION_ALREADY_SENT") {
		t.Fatalf("repeat status=%d body=%s", w.Code, w.Body.String())
	}
	if len(submit.calls) != 2 {
		t.Fatalf("storm sends=%d", len(submit.calls))
	}
	// SEQUENCE bump via update re-arms the gate.
	update := `{"version":1,"summary":"Standup 2","starts_at":"2026-03-01T09:00:00Z","ends_at":"2026-03-01T10:00:00Z","timezone":"UTC","attendees":["ada@example.test","bob@example.test"]}`
	if w := invitationRequest(r, http.MethodPut, "/api/v1/mailbox/calendars/events/42", update); w.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", w.Code, w.Body.String())
	}
	if w := invitationRequest(r, http.MethodPost, "/api/v1/mailbox/calendars/events/42/invitations", body); w.Code != http.StatusAccepted {
		t.Fatalf("after bump status=%d", w.Code)
	}
	if len(submit.calls) != 4 || !strings.Contains(string(submit.calls[3].Attachments[0].Content), "SEQUENCE:1") {
		t.Fatalf("sends=%d", len(submit.calls))
	}
}

func TestInvitationHTTPCancelFlow(t *testing.T) {
	*defaultInvitationGate = *newInvitationGate()
	submit := &fakeInvitationSubmitter{}
	store := &fakeCalendarStore{}
	row := seedInvitedEvent(t, store)
	r, _, _ := invitationHandler(t, store, submit)
	// Cancel first flips the stored status via the normal update path.
	update := `{"version":1,"summary":"Standup","starts_at":"2026-03-01T09:00:00Z","ends_at":"2026-03-01T10:00:00Z","timezone":"UTC","status":"cancelled","attendees":["ada@example.test","bob@example.test"]}` //nolint:misspell // RFC 5545 STATUS value in JSON payload
	if w := invitationRequest(r, http.MethodPut, "/api/v1/mailbox/calendars/events/42", update); w.Code != http.StatusOK {
		t.Fatalf("update status=%d", w.Code)
	}
	// REQUEST on an already-canceled-status event is refused.
	w := invitationRequest(r, http.MethodPost, "/api/v1/mailbox/calendars/events/42/invitations", `{"confirm":true,"method":"request"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("request-on-canceled-event status=%d", w.Code)
	}
	w = invitationRequest(r, http.MethodPost, "/api/v1/mailbox/calendars/events/42/invitations", `{"confirm":true,"method":"cancel"}`)
	if w.Code != http.StatusAccepted || len(submit.calls) != 2 {
		t.Fatalf("cancel status=%d sends=%d", w.Code, len(submit.calls))
	}
	ics := string(submit.calls[0].Attachments[0].Content)
	// The double-L spelling is the RFC 5545 STATUS wire token.
	if !strings.Contains(ics, "METHOD:CANCEL") || !strings.Contains(ics, "STATUS:CANCELLED") || strings.Contains(ics, "RSVP=TRUE") { //nolint:misspell // RFC 5545 STATUS value
		t.Fatalf("cancel ics %q", ics)
	}
	_ = row
	// Repeat cancel for the same SEQUENCE is refused.
	if w := invitationRequest(r, http.MethodPost, "/api/v1/mailbox/calendars/events/42/invitations", `{"confirm":true,"method":"cancel"}`); w.Code != http.StatusConflict {
		t.Fatalf("repeat cancel status=%d", w.Code)
	}
}

func TestDeleteEventWithNotifySendsCancel(t *testing.T) {
	*defaultInvitationGate = *newInvitationGate()
	submit := &fakeInvitationSubmitter{}
	store := &fakeCalendarStore{}
	seedInvitedEvent(t, store)
	r, _, _ := invitationHandler(t, store, submit)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/api/v1/mailbox/calendars/events/42?version=1&notify=attendees", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || len(store.deleted) != 1 {
		t.Fatalf("status=%d deleted=%d body=%s", w.Code, len(store.deleted), w.Body.String())
	}
	if len(submit.calls) != 2 {
		t.Fatalf("cancel sends=%d", len(submit.calls))
	}
	if !strings.Contains(string(submit.calls[0].Attachments[0].Content), "METHOD:CANCEL") {
		t.Fatalf("no cancel notice: %q", submit.calls[0].Attachments[0].ContentType)
	}
}

func TestIngestReplyRequiresVerifiedMailboxMessage(t *testing.T) {
	attendees, _ := json.Marshal([]string{"ada@example.test", "bob@example.test"})
	st := &fakeCalendarStore{}
	st.put(store.MailboxEvents{
		ID: 42, TenantID: 7, MailboxID: 9, Uid: "uid-42@urmail", Version: 1, Sequence: 0,
		Summary: "Standup", StartsAt: timestamptz(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)),
		EndsAt:   timestamptz(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)),
		Timezone: "UTC", Status: "confirmed", Attendees: attendees,
	})
	r, _, _ := invitationHandler(t, st, &fakeInvitationSubmitter{})
	body := "BEGIN:VCALENDAR\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:uid-42@urmail\r\nATTENDEE;PARTSTAT=ACCEPTED:mailto:bob@example.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	w := invitationRequest(r, http.MethodPost, "/api/v1/mailbox/calendars/events/42/reply", body)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "INVITATION_SOURCE_REQUIRED") {
		t.Fatalf("raw claimed attendee was trusted: %d %s", w.Code, w.Body.String())
	}
	_, status := appcollab.DecodeAttendees(st.stored[42].Attendees)
	if status["bob@example.test"] == collab.PartStatAccepted {
		t.Fatal("raw response changed attendee")
	}
}

// defaultInvitationGate rate limits invitation bursts for every calendar
// handler in this process: one burst per event UID, SEQUENCE and method, so
// re-POSTing the same event version cannot storm the mail system. Sending
// again becomes possible after the next SEQUENCE bump (an event update).
var defaultInvitationGate = newInvitationGate()

type invitationGate struct {
	mu   sync.Mutex
	sent map[string]bool
}

func newInvitationGate() *invitationGate {
	return &invitationGate{sent: map[string]bool{}}
}

func (g *invitationGate) key(uid string, sequence int64, method string) string {
	return fmt.Sprintf("%s|%d|%s", uid, sequence, method)
}

// allow claims the one-shot slot; it returns false when this exact
// invitation already went out.
func (g *invitationGate) allow(uid string, sequence int64, method string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := g.key(uid, sequence, method)
	if g.sent[key] {
		return false
	}
	g.sent[key] = true
	return true
}
