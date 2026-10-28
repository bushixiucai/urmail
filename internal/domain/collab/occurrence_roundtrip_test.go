package collab

import (
	"strings"
	"testing"
	"time"
)

func TestRecurringWallClockOverridesAndStandardRoundTrip(t *testing.T) {
	start := time.Date(2026, 3, 7, 14, 0, 0, 0, time.UTC)
	event := Event{UID: "series@example.test", Summary: "Daily local 9am", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: "America/New_York", RRule: "FREQ=DAILY;COUNT=3", Overrides: []OccurrenceOverride{{RecurrenceID: start.Add(23 * time.Hour), Summary: "Only this meeting", StartsAt: time.Date(2026, 3, 8, 16, 0, 0, 0, time.UTC), EndsAt: time.Date(2026, 3, 8, 17, 0, 0, 0, time.UTC), Status: "confirmed"}}}
	got, err := ExpandInstances(event, start.Add(-time.Hour), start.Add(4*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1].UTC().Format(time.RFC3339) != "2026-03-08T16:00:00Z" || got[2].UTC().Format(time.RFC3339) != "2026-03-09T13:00:00Z" {
		t.Fatalf("DST/override=%v", got)
	}
	raw, err := ICalendar(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(raw, "BEGIN:VEVENT") != 2 || strings.Count(raw, "UID:series@example.test") != 2 || !strings.Contains(raw, "RECURRENCE-ID:20260308T130000Z") {
		t.Fatal("invalid recurrence resource")
	}
	parsed, err := ParseICalendar(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.UID != event.UID || parsed.Timezone != event.Timezone || len(parsed.Overrides) != 1 || parsed.Overrides[0].Summary != "Only this meeting" {
		t.Fatalf("lost roundtrip: %+v", parsed)
	}
}

func TestAllDayZonedDatesAndFoldedVCardRoundTrip(t *testing.T) {
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	event := Event{Summary: "All day", StartsAt: time.Date(2026, 10, 28, 0, 0, 0, 0, zone), EndsAt: time.Date(2026, 10, 29, 0, 0, 0, 0, zone), AllDay: true, Timezone: zone.String()}
	raw, err := ICalendar(event)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseICalendar(raw)
	if err != nil || !parsed.StartsAt.Equal(event.StartsAt) || !parsed.EndsAt.Equal(event.EndsAt) || !parsed.AllDay {
		t.Fatalf("all-day drift: %+v %v", parsed, err)
	}
	contact := Contact{DisplayName: strings.Repeat("多值联系人", 20), GivenName: "Given;name", FamilyName: "Family", Emails: []Email{{Address: "one@example.test", Label: "work"}, {Address: "two@example.test", Label: "custom label"}}, Phones: []Phone{{Number: "+1 555 0100", Label: "home"}}, Note: strings.Repeat("多行备注", 40) + "\nSecond line"}
	card, err := VCard(contact)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(card, "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line exceeds 75 octets: %d", len(line))
		}
	}
	parsedContact, err := ParseVCard(card)
	if err != nil || parsedContact.GivenName != contact.GivenName || len(parsedContact.Emails) != 2 || parsedContact.Emails[0].Label != "work" || parsedContact.Emails[1].Label != "custom label" || parsedContact.Note != contact.Note {
		t.Fatalf("folded multi-value contact lost: %+v %v", parsedContact, err)
	}
}
