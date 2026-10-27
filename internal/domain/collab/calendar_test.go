package collab

import (
	"errors"
	"testing"
	"time"
)

func TestNormalizeEventRejectsUnboundedAndInverted(t *testing.T) {
	start := time.Date(2026, 3, 8, 1, 30, 0, 0, time.FixedZone("X", 8*3600))
	got, err := NormalizeEvent(Event{
		Summary:  "Standup",
		StartsAt: start,
		EndsAt:   start.Add(30 * time.Minute),
		Timezone: "Asia/Shanghai",
		RRule:    "FREQ=WEEKLY;COUNT=10",
	})
	if err != nil || got.StartsAt.Location() != time.UTC || got.Timezone != "Asia/Shanghai" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := NormalizeEvent(Event{Summary: "x", StartsAt: start, EndsAt: start, Timezone: "UTC"}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal("inverted accepted")
	}
	if _, err := NormalizeEvent(Event{Summary: "x", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: "UTC", RRule: "FREQ=DAILY;COUNT=99999"}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal("huge count accepted")
	}
	if _, err := NormalizeEvent(Event{Summary: "x", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: "Not/AZone"}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal("bad zone accepted")
	}
}

func TestMalformedExceptionIsRejected(t *testing.T) {
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	_, err := NormalizeEvent(Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour), RRule: "FREQ=DAILY;EXDATE=tomorrow"})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatal(err)
	}
}

func TestAttendeesStayOnEventWithoutInvites(t *testing.T) {
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	got, err := NormalizeEvent(Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour), Attendees: []string{"Ada@Example.test"}})
	if err != nil || len(got.Attendees) != 1 || got.Attendees[0] != "ada@example.test" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestInvalidAttendeeIsRejected(t *testing.T) {
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	_, err := NormalizeEvent(Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour), Attendees: []string{"not-an-email"}})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatal(err)
	}
}
