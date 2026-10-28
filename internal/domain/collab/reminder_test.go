package collab

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDisplayReminderRoundTripAndLimits(t *testing.T) {
	start := time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC)
	event := Event{UID: "alarm@example.test", Summary: "DST reminder", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: "America/New_York", ReminderMinutes: []int{0, 15, 1440}}
	raw, err := ICalendar(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "TRIGGER:-PT15M") || !strings.Contains(raw, "ACTION:DISPLAY") {
		t.Fatal("DISPLAY alarm absent")
	}
	parsed, err := ParseICalendar(raw)
	if err != nil || !reflect.DeepEqual(parsed.ReminderMinutes, event.ReminderMinutes) {
		t.Fatalf("roundtrip %v %v", parsed.ReminderMinutes, err)
	}
	for _, minutes := range [][]int{{-1}, {40321}, {15, 15}, {1, 2, 3, 4, 5, 6}} {
		event.ReminderMinutes = minutes
		if _, err = NormalizeEvent(event); err == nil {
			t.Fatalf("invalid alarm accepted %v", minutes)
		}
	}
	if _, ok := parseDisplayAlarm([]string{"ACTION:EMAIL", "TRIGGER:-PT15M", "DESCRIPTION:external"}); ok {
		t.Fatal("external alarm was enabled")
	}
	if minutes, ok := parseDisplayAlarm([]string{"ACTION:DISPLAY", "TRIGGER:-P1DT2H30M", "DESCRIPTION:bounded"}); !ok || minutes != 1590 {
		t.Fatalf("day duration %d %v", minutes, ok)
	}
}
