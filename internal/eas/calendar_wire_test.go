package eas

import (
	"encoding/base64"
	"encoding/binary"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/collab"
)

func TestCalendarWirePrimarySpec(t *testing.T) {
	// CompactDateTime, DayOfWeek=Monday(2), sibling Exceptions, and the
	// 172-byte TIME_ZONE_INFORMATION layout are specified by MS-ASCAL/ASDTYPE.
	loc, _ := time.LoadLocation("America/New_York")
	start := time.Date(2026, 3, 2, 9, 0, 0, 0, loc)
	e := collab.Event{UID: "meeting@example.test", Summary: "Weekly", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: loc.String(), RRule: "FREQ=WEEKLY;INTERVAL=2;COUNT=5", Status: "confirmed", Overrides: []collab.OccurrenceOverride{{RecurrenceID: start.AddDate(0, 0, 14), Summary: "Moved", StartsAt: start.AddDate(0, 0, 14).Add(time.Hour), EndsAt: start.AddDate(0, 0, 14).Add(2 * time.Hour), Status: "confirmed"}}}
	rec, ok := parseRecurrence(e.RRule)
	if !ok {
		t.Fatal("weekly interval lost")
	}
	node := calendarAppData(e, time.Time{}, &rec, e.UID)
	if calendarText(node, tokCalStartTime) != "20260302T140000Z" {
		t.Fatal("calendar used extended dateTime")
	}
	rn := calendarChild(node, pageCalendar, tokCalRecurrence)
	if calendarText(rn, tokCalDayOfWeek) != "2" {
		t.Fatal("required weekday missing")
	}
	if calendarChild(rn, pageCalendar, tokCalExceptions) != nil || calendarChild(node, pageCalendar, tokCalExceptions) == nil {
		t.Fatal("Exceptions are not ApplicationData siblings")
	}
	raw, err := base64.StdEncoding.DecodeString(calendarText(node, tokCalTimezone))
	if err != nil || len(raw) != 172 {
		t.Fatal("invalid timezone structure")
	}
	if int32(binary.LittleEndian.Uint32(raw[:4])) != 300 || int32(binary.LittleEndian.Uint32(raw[168:])) != -60 {
		t.Fatal("New York DST bias lost")
	}
	round, err := parseCalendarData(node, collab.Event{})
	if err != nil {
		t.Fatal(err)
	}
	if round.Timezone != e.Timezone || len(round.Overrides) != 1 || !round.Overrides[0].StartsAt.Equal(e.Overrides[0].StartsAt) {
		t.Fatalf("timezone/override lost: %+v", round)
	}
	for _, name := range []string{"UTC", "Asia/Shanghai", "Asia/Kathmandu", "Australia/Lord_Howe", "Pacific/Auckland"} {
		wire := calendarZoneWire(name, start)
		decoded, err := parseCalendarZone(wire, start)
		if err != nil || decoded != name {
			t.Fatalf("timezone %s roundtrip=%s err=%v", name, decoded, err)
		}
	}
	for _, typ := range []string{"3", "6"} {
		if _, err := calendarRule(Element(pageCalendar, tokCalRecurrence, Text(pageCalendar, tokCalRecType, typ))); err == nil {
			t.Fatal("nth pattern silently changed")
		}
	}
	if _, err := parseCalendarData(Element(pageAirSync, tokApplicationData, Text(pageCalendar, tokCalStartTime, "2026-03-02T14:00:00.000Z")), e); err == nil {
		t.Fatal("accepted non-compact calendar date")
	}
}
func TestCalendarTimezoneAnonymousRulesAndAllDay(t *testing.T) {
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	raw, _ := base64.StdEncoding.DecodeString(calendarZoneWire("America/New_York", at))
	clear(raw[4:68])
	clear(raw[88:152])
	name, err := parseCalendarZone(base64.StdEncoding.EncodeToString(raw), at)
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation(name)
	before := time.Date(2026, 3, 2, 9, 0, 0, 0, loc)
	after := before.AddDate(0, 0, 14)
	if after.Sub(before) == 14*24*time.Hour {
		t.Fatal("anonymous timezone mapped to fixed offset, DST lost")
	}
	e := collab.Event{Summary: "All day", StartsAt: time.Date(2026, 3, 2, 5, 0, 0, 0, time.UTC), EndsAt: time.Date(2026, 3, 3, 5, 0, 0, 0, time.UTC), AllDay: true, Timezone: "America/New_York"}
	if _, err := parseCalendarData(calendarAppData(e, time.Time{}, nil, "day"), collab.Event{}); err != nil {
		t.Fatal(err)
	}
	e.StartsAt = e.StartsAt.Add(time.Hour)
	if _, err := parseCalendarData(calendarAppData(e, time.Time{}, nil, "day"), collab.Event{}); err == nil {
		t.Fatal("non-midnight all-day accepted")
	}
}
