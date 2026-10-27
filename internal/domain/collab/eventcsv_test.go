package collab

import (
	"errors"
	"strings"
	"testing"
)

func TestParseEventsCSV(t *testing.T) {
	got, err := ParseEventsCSV("summary,starts_at,ends_at\nPlan,2026-10-26T09:00:00Z,2026-10-26T10:00:00Z\n")
	if err != nil || len(got) != 1 || got[0].Summary != "Plan" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestParseEventsCSVRejectsInvertedTime(t *testing.T) {
	if _, err := ParseEventsCSV("Plan,2026-10-26T10:00:00Z,2026-10-26T09:00:00Z\n"); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal(err)
	}
}

func TestCSVRejectsExceptionOutsideSeries(t *testing.T) {
	raw := "summary,start,end,rule\nA,2026-03-01T09:00:00Z,2026-03-01T10:00:00Z,FREQ=DAILY;COUNT=2;EXDATE=20260305T090000Z\n"
	_, err := ParseEventsCSV(raw)
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatal(err)
	}
}

func TestCSVKeepsValidExceptionOnOriginalEvent(t *testing.T) {
	raw := "summary,start,end,rule\nA,2026-03-01T09:00:00Z,2026-03-01T10:00:00Z,FREQ=DAILY;COUNT=3;EXDATE=20260302T090000Z\n"
	got, err := ParseEventsCSV(raw)
	if err != nil || len(got) != 1 || got[0].Summary != "A" || !strings.Contains(got[0].RRule, "EXDATE=20260302T090000Z") {
		t.Fatalf("%+v %v", got, err)
	}
}
