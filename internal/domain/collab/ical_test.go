package collab

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestICalendarRoundTrip(t *testing.T) {
	start := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	raw, err := ICalendar(Event{Summary: "Plan; review", StartsAt: start, EndsAt: start.Add(time.Hour), RRule: "FREQ=DAILY;COUNT=3"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseICalendar(raw)
	if err != nil || got.Summary != "Plan; review" || got.RRule != "FREQ=DAILY;COUNT=3" || !got.StartsAt.Equal(start) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestParseICalendarRejectsTwoEvents(t *testing.T) {
	one, err := ICalendar(Event{Summary: "A", StartsAt: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC), EndsAt: time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseICalendar(one + one); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal(err)
	}
}

func TestAttendeeRoundTripStaysOnEvent(t *testing.T) {
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	raw, err := ICalendar(Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour), Attendees: []string{"ada@example.test"}})
	if err != nil || strings.Contains(raw, "METHOD:REQUEST") {
		t.Fatal(err)
	}
	got, err := ParseICalendar(raw)
	if err != nil || len(got.Attendees) != 1 || got.Attendees[0] != "ada@example.test" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestExceptionExportsAsOwnLine(t *testing.T) {
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	raw, err := ICalendar(Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour), RRule: "FREQ=DAILY;COUNT=3;EXDATE=20260302T090000Z"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "RRULE:FREQ=DAILY;COUNT=3\r\n") || !strings.Contains(raw, "EXDATE:20260302T090000Z") {
		t.Fatal(raw)
	}
	got, err := ParseICalendar(raw)
	if err != nil || !strings.Contains(got.RRule, "EXDATE=20260302T090000Z") {
		t.Fatalf("%+v %v", got, err)
	}
}
