package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestCalendarInvitationDurableSMTPAndCancelCASPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	svc := appcollab.Service{Q: store.New(f.pool)}
	p := appcollab.Principal{TenantID: f.principal.TenantID, MailboxID: f.principal.MailboxID}
	cal, err := svc.EnsureCalendar(t.Context(), p, "Calendar", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	create := func(summary string) store.MailboxEvents {
		start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
		event := collab.Event{Summary: summary, StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: "UTC", RRule: "FREQ=WEEKLY;COUNT=3", Attendees: []string{"attendee@example.test"}, Overrides: []collab.OccurrenceOverride{{RecurrenceID: start.AddDate(0, 0, 7), Summary: "Moved instance", StartsAt: start.AddDate(0, 0, 7).Add(time.Hour), EndsAt: start.AddDate(0, 0, 7).Add(2 * time.Hour), Status: "confirmed"}}}
		row, err := svc.CreateEvent(t.Context(), p, cal.ID, "", event)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := store.New(f.pool).PlatformGetEvent(t.Context(), store.PlatformGetEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: row.ID})
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	submit := &fakeInvitationSubmitter{}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		// Every request constructs a fresh handler: duplicate prevention must be PG.
		h := MailboxCalendarHandler{Pool: f.pool, Service: svc, Submitter: submit, Vault: f.native.Vault}
		r := gin.New()
		auth := r.Group("/api/v1/mailbox")
		auth.Use(middleware.RequireMailboxAuth(StoreMailboxNative{Q: store.New(f.pool)}, f.native.Vault))
		auth.POST("/calendars/events/:id/invitations", h.SendInvitations)
		auth.DELETE("/calendars/events/:id", h.DeleteEvent)
		req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	row := create("Send once")
	path := fmt.Sprintf("/api/v1/mailbox/calendars/events/%d/invitations", row.ID)
	if rec := request("POST", path, `{"confirm":true}`); rec.Code != 202 {
		t.Fatalf("send %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("POST", path, `{"confirm":true}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "INVITATION_ALREADY_SENT") || len(submit.calls) != 1 {
		t.Fatal("restart repeated invitation")
	}
	if !strings.Contains(string(submit.calls[0].Attachments[0].Content), "RECURRENCE-ID:") {
		t.Fatal("invitation omitted single-instance override")
	}
	unknown := create("Uncertain")
	submit.err = webmail.ErrSubmitUncertain
	unknownPath := fmt.Sprintf("/api/v1/mailbox/calendars/events/%d/invitations", unknown.ID)
	if rec := request("POST", unknownPath, `{"confirm":true}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "MAILBOX_SEND_UNCERTAIN") {
		t.Fatalf("unknown result retryable: %d %s", rec.Code, rec.Body.String())
	}
	submit.err = nil
	if rec := request("POST", unknownPath, `{"confirm":true}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "MAILBOX_SEND_UNCERTAIN") || len(submit.calls) != 1 {
		t.Fatal("unknown result resent after restart")
	}
	cancel := create("Cancel safely")
	base := fmt.Sprintf("/api/v1/mailbox/calendars/events/%d", cancel.ID)
	if rec := request("DELETE", base+"?version=999&notify=attendees", ""); rec.Code != 409 || len(submit.calls) != 1 {
		t.Fatal("stale cancel sent before CAS")
	}
	if rec := request("DELETE", base+"?version=1&notify=attendees", ""); rec.Code != 200 || len(submit.calls) != 2 {
		t.Fatalf("default submission cancel %d %s", rec.Code, rec.Body.String())
	}
	if _, err := store.New(f.pool).PlatformGetEvent(t.Context(), store.PlatformGetEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: cancel.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cancel did not tombstone")
	}
	notice := string(submit.calls[1].Attachments[0].Content)
	if !strings.Contains(notice, "METHOD:CANCEL") || !strings.Contains(notice, "SEQUENCE:1") || !strings.Contains(notice, "RECURRENCE-ID:") {
		t.Fatal("cancel lost sequence or overrides")
	}
	tx, err := f.pool.MailboxTx(t.Context(), p.TenantID, p.MailboxID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var states []string
	rows, err := tx.Query(t.Context(), "SELECT status FROM mailbox_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND kind='calendar_invitation' ORDER BY id", p.TenantID, p.MailboxID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatal(err)
		}
		states = append(states, status)
	}
	rows.Close()
	encoded, _ := json.Marshal(states)
	if string(encoded) != `["done","uncertain","done"]` {
		t.Fatalf("durable journal states=%s", encoded)
	}
}
