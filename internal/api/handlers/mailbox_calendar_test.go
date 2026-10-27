package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type fakeCalendarStore struct {
	calendars []store.MailboxCalendars
	events    []store.PlatformInsertEventRow
	listed    []store.MailboxEvents
	nextID    int64
	stored    map[int64]store.MailboxEvents
	deleted   []store.PlatformDeleteEventParams
}

func (f *fakeCalendarStore) put(row store.MailboxEvents) {
	if f.stored == nil {
		f.stored = map[int64]store.MailboxEvents{}
	}
	f.stored[row.ID] = row
}

func (f *fakeCalendarStore) PlatformEnsureAddressbook(context.Context, store.PlatformEnsureAddressbookParams) (store.MailboxAddressbooks, error) {
	return store.MailboxAddressbooks{}, pgx.ErrNoRows
}
func (f *fakeCalendarStore) PlatformGetAddressbookByName(context.Context, store.PlatformGetAddressbookByNameParams) (store.MailboxAddressbooks, error) {
	return store.MailboxAddressbooks{}, pgx.ErrNoRows
}
func (f *fakeCalendarStore) PlatformInsertContact(context.Context, store.PlatformInsertContactParams) (store.PlatformInsertContactRow, error) {
	return store.PlatformInsertContactRow{}, pgx.ErrNoRows
}
func (f *fakeCalendarStore) PlatformUpdateContact(context.Context, store.PlatformUpdateContactParams) (store.PlatformUpdateContactRow, error) {
	return store.PlatformUpdateContactRow{}, pgx.ErrNoRows
}
func (f *fakeCalendarStore) PlatformGetContact(context.Context, store.PlatformGetContactParams) (store.MailboxContacts, error) {
	return store.MailboxContacts{}, pgx.ErrNoRows
}
func (f *fakeCalendarStore) PlatformListContacts(context.Context, store.PlatformListContactsParams) ([]store.MailboxContacts, error) {
	return nil, nil
}
func (f *fakeCalendarStore) PlatformSearchContacts(context.Context, store.PlatformSearchContactsParams) ([]store.MailboxContacts, error) {
	return nil, nil
}
func (f *fakeCalendarStore) PlatformDeleteContact(context.Context, store.PlatformDeleteContactParams) (store.PlatformDeleteContactRow, error) {
	return store.PlatformDeleteContactRow{}, pgx.ErrNoRows
}
func (f *fakeCalendarStore) PlatformEnsureCalendar(_ context.Context, arg store.PlatformEnsureCalendarParams) (store.MailboxCalendars, error) {
	for _, cal := range f.calendars {
		if cal.TenantID == arg.TenantID && cal.MailboxID == arg.MailboxID && cal.Name == arg.Name {
			return store.MailboxCalendars{}, pgx.ErrNoRows
		}
	}
	f.nextID++
	cal := store.MailboxCalendars{ID: f.nextID, TenantID: arg.TenantID, MailboxID: arg.MailboxID, Name: arg.Name, Timezone: arg.Timezone, Version: 1}
	f.calendars = append(f.calendars, cal)
	return cal, nil
}
func (f *fakeCalendarStore) PlatformGetCalendarByName(_ context.Context, arg store.PlatformGetCalendarByNameParams) (store.MailboxCalendars, error) {
	for _, cal := range f.calendars {
		if cal.TenantID == arg.TenantID && cal.MailboxID == arg.MailboxID && cal.Name == arg.Name {
			return cal, nil
		}
	}
	return store.MailboxCalendars{}, pgx.ErrNoRows
}
func (f *fakeCalendarStore) PlatformInsertEvent(_ context.Context, arg store.PlatformInsertEventParams) (store.PlatformInsertEventRow, error) {
	f.nextID++
	row := store.PlatformInsertEventRow{
		ID: f.nextID, TenantID: arg.TenantID, MailboxID: arg.MailboxID, CalendarID: arg.CalendarID,
		Uid: arg.Uid, Version: 1, Summary: arg.Summary, StartsAt: arg.StartsAt, EndsAt: arg.EndsAt,
		Timezone: arg.Timezone, Status: arg.Status,
	}
	f.events = append(f.events, row)
	f.put(store.MailboxEvents{
		ID: row.ID, TenantID: row.TenantID, MailboxID: row.MailboxID, CalendarID: row.CalendarID,
		Uid: row.Uid, Version: row.Version, Summary: row.Summary, StartsAt: row.StartsAt,
		EndsAt: row.EndsAt, Timezone: row.Timezone, Status: row.Status,
	})
	return row, nil
}
func (f *fakeCalendarStore) PlatformUpdateEvent(_ context.Context, arg store.PlatformUpdateEventParams) (store.PlatformUpdateEventRow, error) {
	row, ok := f.stored[arg.ID]
	if !ok || row.Version != arg.ExpectedVersion {
		return store.PlatformUpdateEventRow{}, pgx.ErrNoRows
	}
	row.Summary = arg.Summary
	row.Description = arg.Description
	row.Location = arg.Location
	row.StartsAt = arg.StartsAt
	row.EndsAt = arg.EndsAt
	row.AllDay = arg.AllDay
	row.Timezone = arg.Timezone
	row.Rrule = arg.Rrule
	row.Status = arg.Status
	row.Attendees = arg.Attendees
	row.Version++
	row.Sequence++
	f.put(row)
	return store.PlatformUpdateEventRow{
		ID: row.ID, TenantID: row.TenantID, MailboxID: row.MailboxID, CalendarID: row.CalendarID,
		Uid: row.Uid, Version: row.Version, Sequence: row.Sequence, Summary: row.Summary,
		StartsAt: row.StartsAt, EndsAt: row.EndsAt, Timezone: row.Timezone, Status: row.Status,
	}, nil
}
func (f *fakeCalendarStore) PlatformGetEvent(_ context.Context, arg store.PlatformGetEventParams) (store.MailboxEvents, error) {
	if row, ok := f.stored[arg.ID]; ok && row.TenantID == arg.TenantID && row.MailboxID == arg.MailboxID {
		return row, nil
	}
	return store.MailboxEvents{}, pgx.ErrNoRows
}
func (f *fakeCalendarStore) PlatformListEvents(context.Context, store.PlatformListEventsParams) ([]store.MailboxEvents, error) {
	if len(f.listed) > 0 {
		return f.listed, nil
	}
	out := make([]store.MailboxEvents, 0, len(f.events))
	for _, row := range f.events {
		out = append(out, store.MailboxEvents{ID: row.ID, TenantID: row.TenantID, MailboxID: row.MailboxID, CalendarID: row.CalendarID, Uid: row.Uid, Version: row.Version, Summary: row.Summary, StartsAt: row.StartsAt, EndsAt: row.EndsAt, Timezone: row.Timezone, Status: row.Status})
	}
	return out, nil
}
func (f *fakeCalendarStore) PlatformDeleteEvent(_ context.Context, arg store.PlatformDeleteEventParams) (store.PlatformDeleteEventRow, error) {
	row, ok := f.stored[arg.ID]
	if !ok || row.Version != arg.Version {
		return store.PlatformDeleteEventRow{}, pgx.ErrNoRows
	}
	f.deleted = append(f.deleted, arg)
	delete(f.stored, arg.ID)
	return store.PlatformDeleteEventRow{ID: row.ID, TenantID: row.TenantID, MailboxID: row.MailboxID, Uid: row.Uid, Version: row.Version, Status: row.Status, Attendees: row.Attendees}, nil
}
func (f *fakeCalendarStore) PlatformListChanges(context.Context, store.PlatformListChangesParams) ([]store.MailboxChanges, error) {
	return nil, nil
}

func TestMailboxCalendarRequireSessionAndStayInMailbox(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	sessions := &fakeMailboxSessions{}
	store := &fakeCalendarStore{}
	native := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash}, Sessions: sessions, Vault: webmail.NewMemoryVault()}
	calendar := MailboxCalendarHandler{Service: appcollab.Service{Q: store}}
	r := newNativeTestRouter(t, native, sessions)
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	auth.GET("/calendars/events", calendar.ListEvents)
	auth.POST("/calendars/events", calendar.CreateEvent)

	open := httptest.NewRecorder()
	r.ServeHTTP(open, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/calendars/events", nil))
	if open.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", open.Code)
	}
	token := loginNative(t, r, "alice@example.test", password)
	body := bytes.NewBufferString(`{"summary":"Standup","starts_at":"2026-03-02T09:00:00Z","ends_at":"2026-03-02T10:00:00Z","timezone":"UTC"}`)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/calendars/events", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	r.ServeHTTP(created, req)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if len(store.events) != 1 || store.events[0].MailboxID != 9 || store.events[0].TenantID != 7 {
		t.Fatalf("stored=%+v", store.events)
	}
	listReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/calendars/events", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listed := httptest.NewRecorder()
	r.ServeHTTP(listed, listReq)
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte("Standup")) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
}

func TestExportCalendarICSMergesAllEvents(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	sessions := &fakeMailboxSessions{}
	starts := pgtype.Timestamptz{Time: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), Valid: true}
	ends := pgtype.Timestamptz{Time: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC), Valid: true}
	seed := &fakeCalendarStore{listed: []store.MailboxEvents{
		{ID: 1, TenantID: 7, MailboxID: 9, Uid: "uid-1", Version: 1, Summary: "One", StartsAt: starts, EndsAt: ends, Timezone: "UTC", Status: "confirmed"},
		{ID: 2, TenantID: 7, MailboxID: 9, Uid: "uid-2", Version: 1, Summary: "Two", StartsAt: starts, EndsAt: ends, Timezone: "UTC", Status: "confirmed"},
	}}
	native := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash}, Sessions: sessions, Vault: webmail.NewMemoryVault()}
	calendar := MailboxCalendarHandler{Service: appcollab.Service{Q: seed}}
	r := newNativeTestRouter(t, native, sessions)
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	auth.GET("/calendars/export.ics", calendar.ExportCalendarICS)
	token := loginNative(t, r, "alice@example.test", password)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/calendars/export.ics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Count(body, "BEGIN:VEVENT") != 2 || strings.Count(body, "BEGIN:VCALENDAR") != 1 || !strings.Contains(body, "One") || !strings.Contains(body, "Two") {
		t.Fatalf("unexpected ics body: %q", body)
	}
}
