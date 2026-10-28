package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// TestCalendarReminderBrowserFixturePG retains real mailbox sessions, RLS,
// event services, the scheduler and HTTP APIs for an isolated browser. Its IMAP
// login transport is an explicit fixture; notification persistence is real PG.
func TestCalendarReminderBrowserFixturePG(t *testing.T) {
	dir := os.Getenv("URMAIL_CALENDAR_BROWSER_FIXTURE_DIR")
	if dir == "" {
		t.Skip("browser fixture not requested")
	}
	f := newNativeAIHTTPFixture(t)
	q := store.New(f.pool)
	service := appcollab.Service{Q: q}
	h := MailboxCalendarHandler{Pool: f.pool, Service: service, Native: f.native, Submitter: f.native.Submitter, Vault: f.native.Vault}
	group := f.router.Group("/api/v1/mailbox", middleware.RequireMailboxAuth(StoreMailboxNative{Q: q}, f.native.Vault))
	group.GET("/calendars/events", h.ListEvents)
	group.POST("/calendars/events", h.CreateEvent)
	group.PUT("/calendars/events/:id", h.UpdateEvent)
	RegisterCalendarReminderRoutes(group, h)
	p := appcollab.Principal{TenantID: f.principal.TenantID, MailboxID: f.principal.MailboxID}
	calendar, err := service.EnsureCalendar(t.Context(), p, "Calendar", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(3 * time.Minute)
	event, err := service.CreateEvent(t.Context(), p, calendar.ID, "", collab.Event{Summary: "Actual persisted browser reminder", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: "UTC", ReminderMinutes: []int{5}})
	if err != nil {
		t.Fatal(err)
	}
	if err = (appcollab.ReminderWorker{Pool: f.pool}).RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	token := loginNative(t, f.router, f.principal.Address, f.password)
	server := httptest.NewServer(f.router)
	defer server.Close()
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "address": f.principal.Address, "event_id": event.ID, "transport": "explicit IMAP login stand-in; actual PostgreSQL sessions and calendar/reminder services"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
			if _, err = os.Stat(filepath.Join(dir, "stop")); err == nil {
				return
			}
		}
	}
}
