package handlers

import (
	"encoding/json"
	"testing"
	"time"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestCalendarReminderPersistenceDedupAndMailboxIsolationPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	q := store.New(f.pool)
	svc := appcollab.Service{Q: q}
	p := appcollab.Principal{TenantID: f.principal.TenantID, MailboxID: f.principal.MailboxID}
	calendar, err := svc.EnsureCalendar(t.Context(), p, "Calendar", "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(3 * time.Minute).Truncate(time.Second)
	event := collab.Event{Summary: "Authorized reminder", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: "America/New_York", RRule: "FREQ=DAILY;COUNT=3", ReminderMinutes: []int{5}}
	created, err := svc.CreateEvent(t.Context(), p, calendar.ID, "", event)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = (appcollab.ReminderWorker{Pool: f.pool}).RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := q.PlatformListCalendarReminders(t.Context(), store.PlatformListCalendarRemindersParams{TenantID: p.TenantID, MailboxID: p.MailboxID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("notification duplicated/missing %d %v", len(rows), err)
	}
	var payload struct {
		Summary string `json:"summary"`
		EventID int64  `json:"event_id"`
	}
	if json.Unmarshal(rows[0].Payload, &payload) != nil || payload.EventID != created.ID || payload.Summary != event.Summary {
		t.Fatal("reminder not bound to event")
	}
	other, err := q.PlatformListCalendarReminders(t.Context(), store.PlatformListCalendarRemindersParams{TenantID: p.TenantID, MailboxID: p.MailboxID + 1})
	if err != nil || len(other) != 0 {
		t.Fatal("cross-mailbox reminder visible")
	}
	if n, e := q.PlatformAcknowledgeCalendarReminder(t.Context(), store.PlatformAcknowledgeCalendarReminderParams{TenantID: p.TenantID, MailboxID: p.MailboxID + 1, ID: rows[0].ID}); e != nil || n != 0 {
		t.Fatal("cross mailbox acknowledged reminder")
	}
	if n, e := q.PlatformAcknowledgeCalendarReminder(t.Context(), store.PlatformAcknowledgeCalendarReminderParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: rows[0].ID}); e != nil || n != 1 {
		t.Fatal("own acknowledgment failed")
	}
	if err = (appcollab.ReminderWorker{Pool: f.pool}).RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err = q.PlatformListCalendarReminders(t.Context(), store.PlatformListCalendarRemindersParams{TenantID: p.TenantID, MailboxID: p.MailboxID})
	if err != nil || len(rows) != 0 {
		t.Fatal("acknowledged notification reappeared")
	}
	saved, err := q.PlatformGetEvent(t.Context(), store.PlatformGetEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: created.ID})
	if err != nil {
		t.Fatal(err)
	}
	event, _, err = appcollab.EventFromRow(saved)
	if err != nil {
		t.Fatal(err)
	}
	event.ReminderMinutes = []int{}
	if _, err = svc.UpdateEvent(t.Context(), p, saved.ID, saved.Version, event); err != nil {
		t.Fatal(err)
	}
	if err = (appcollab.ReminderWorker{Pool: f.pool}).RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err = f.pool.QueryRow(t.Context(), "SELECT count(*) FROM mailbox_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND kind='calendar_reminder' AND status='pending'", p.TenantID, p.MailboxID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("removed reminders still pending %d %v", pending, err)
	}
}
