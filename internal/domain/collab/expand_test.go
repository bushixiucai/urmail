package collab

import (
	"errors"
	"testing"
	"time"
)

func TestExpandDailyCount(t *testing.T) {
	start := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	got, err := ExpandInstances(Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour), RRule: "FREQ=DAILY;COUNT=3"}, start, start.AddDate(0, 0, 10))
	if err != nil || len(got) != 3 || !got[2].Equal(start.AddDate(0, 0, 2)) {
		t.Fatalf("%v %v", got, err)
	}
}

func TestExpandRejectsHugeCount(t *testing.T) {
	start := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	if _, err := ExpandInstances(Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour), RRule: "FREQ=DAILY;COUNT=99999"}, start, start.AddDate(0, 0, 10)); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal(err)
	}
}

func TestExcludedDateIsOmitted(t *testing.T) {
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	got, err := ExpandInstances(Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour), RRule: "FREQ=DAILY;COUNT=3;EXDATE=20260302T090000Z"}, start, start.AddDate(0, 0, 10))
	if err != nil || len(got) != 2 || got[1].Day() != 3 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestExceptionOutsideSeriesIsRejected(t *testing.T) {
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	_, err := ExpandInstances(Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour), RRule: "FREQ=DAILY;COUNT=3;EXDATE=20260305T090000Z"}, start, start.AddDate(0, 0, 10))
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatal(err)
	}
}
