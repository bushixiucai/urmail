package billing_test

import (
	"errors"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/billing"
)

func calendarTime(t *testing.T, value, zone string) time.Time {
	t.Helper()
	location, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	instant, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", value, location)
	if err != nil {
		t.Fatal(err)
	}
	return instant
}

func TestCalendarAnchorRestoresClampedDayAndLeapAnniversary(t *testing.T) {
	tests := []struct {
		zone, start, cycle string
		ends               []string
	}{
		{"Asia/Shanghai", "2024-01-31 17:23:41.123456789", billing.CycleMonthly, []string{"2024-02-29 17:23:41.123456789", "2024-03-31 17:23:41.123456789", "2024-04-30 17:23:41.123456789", "2024-05-31 17:23:41.123456789"}},
		{"UTC", "2023-01-31 00:00:00", billing.CycleMonthly, []string{"2023-02-28 00:00:00", "2023-03-31 00:00:00"}},
		{"Asia/Shanghai", "2024-02-29 08:09:10", billing.CycleAnnual, []string{"2025-02-28 08:09:10", "2026-02-28 08:09:10", "2027-02-28 08:09:10", "2028-02-29 08:09:10"}},
		{"UTC", "2024-08-31 12:00:00", billing.CycleQuarterly, []string{"2024-11-30 12:00:00", "2025-02-28 12:00:00", "2025-05-31 12:00:00"}},
	}
	for _, test := range tests {
		t.Run(test.zone+test.start+test.cycle, func(t *testing.T) {
			start := calendarTime(t, test.start, test.zone)
			anchor, err := billing.NewCalendarAnchor(start, test.zone)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range test.ends {
				end, err := billing.AddAnchoredCycle(start.UTC(), test.cycle, anchor)
				if err != nil || !end.Equal(calendarTime(t, want, test.zone)) {
					t.Fatalf("end=%v want=%s error=%v", end, want, err)
				}
				start = end
			}
		})
	}
}

func TestCalendarAnchorDSTIsDeterministicAndRestoresWallClock(t *testing.T) {
	tests := []struct{ name, zone, start, want, next string }{
		{"hour-gap", "America/New_York", "2024-02-10 02:30:00", "2024-03-10T07:30:00Z", "2024-04-10 02:30:00"},
		{"hour-overlap-earliest", "America/New_York", "2024-10-03 01:30:00", "2024-11-03T05:30:00Z", "2024-12-03 01:30:00"},
		{"half-hour-gap", "Australia/Lord_Howe", "2024-09-06 02:15:00", "2024-10-05T15:45:00Z", "2024-11-06 02:15:00"},
		{"half-hour-overlap-earliest", "Australia/Lord_Howe", "2024-03-07 01:45:00", "2024-04-06T14:45:00Z", "2024-05-07 01:45:00"},
		{"skipped-civil-day", "Pacific/Apia", "2011-11-30 12:00:00", "2011-12-30T22:00:00Z", "2012-01-30 12:00:00"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start := calendarTime(t, test.start, test.zone)
			anchor, err := billing.NewCalendarAnchor(start, test.zone)
			if err != nil {
				t.Fatal(err)
			}
			end, err := billing.AddAnchoredCycle(start, billing.CycleMonthly, anchor)
			if err != nil || end.UTC().Format(time.RFC3339) != test.want {
				t.Fatalf("end=%v want=%s error=%v", end, test.want, err)
			}
			next, err := billing.AddAnchoredCycle(end, billing.CycleMonthly, anchor)
			if err != nil || !next.Equal(calendarTime(t, test.next, test.zone)) {
				t.Fatalf("next=%v error=%v", next, err)
			}
		})
	}
}

func TestCalendarRenewalPreservesPaidInstantsAndFrozenZone(t *testing.T) {
	paid := calendarTime(t, "2024-01-31 23:15:00", "Asia/Shanghai")
	anchor, err := billing.NewCalendarAnchor(paid, "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	existing := calendarTime(t, "2024-02-29 23:15:00", "Asia/Shanghai")
	// Different caller location must not reinterpret the persisted billing anchor.
	now := calendarTime(t, "2024-02-01 00:00:00", "America/New_York")
	start, end, err := billing.CalculateAnchoredRenewalPeriod(now, existing.UTC(), billing.CycleMonthly, anchor)
	if err != nil || !start.Equal(existing) || !end.Equal(calendarTime(t, "2024-03-31 23:15:00", "Asia/Shanghai")) {
		t.Fatalf("start=%v end=%v err=%v", start, end, err)
	}
	late := calendarTime(t, "2024-07-12 10:00:00", "Asia/Shanghai")
	start, end, err = billing.CalculateAnchoredRenewalPeriod(late, existing, billing.CycleMonthly, anchor)
	if err != nil || !start.Equal(late) || !end.Equal(calendarTime(t, "2024-08-31 23:15:00", "Asia/Shanghai")) {
		t.Fatalf("lapsed start=%v end=%v err=%v", start, end, err)
	}
}

func TestCalendarRejectsInvalidAnchorAndCycle(t *testing.T) {
	now := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)
	for _, zone := range []string{"", "Local", "Missing/Zone"} {
		if _, err := billing.NewCalendarAnchor(now, zone); !errors.Is(err, billing.ErrInvalidCalendarAnchor) {
			t.Fatal("invalid zone accepted")
		}
	}
	if _, err := billing.NewCalendarAnchor(time.Time{}, "UTC"); !errors.Is(err, billing.ErrInvalidCalendarAnchor) {
		t.Fatal("zero first paid time accepted")
	}
	anchor, _ := billing.NewCalendarAnchor(now, "UTC")
	for _, bad := range []billing.CalendarAnchor{{Timezone: "UTC"}, {Timezone: "UTC", Day: 32}, {Timezone: "UTC", Day: 1, Hour: 24}, {Timezone: "UTC", Day: 1, Minute: 60}, {Timezone: "UTC", Day: 1, Second: 60}, {Timezone: "UTC", Day: 1, Nanosecond: -1}, {Timezone: "UTC", Day: 1, Nanosecond: 1e9}} {
		if _, err := billing.AddAnchoredCycle(now, billing.CycleMonthly, bad); !errors.Is(err, billing.ErrInvalidCalendarAnchor) {
			t.Fatal("invalid anchor accepted")
		}
	}
	if _, err := billing.AddAnchoredCycle(now, "invalid", anchor); !errors.Is(err, billing.ErrInvalidCycle) {
		t.Fatal("invalid cycle accepted")
	}
	if _, _, err := billing.CalculateAnchoredRenewalPeriod(time.Time{}, now, billing.CycleMonthly, anchor); !errors.Is(err, billing.ErrInvalidPeriod) {
		t.Fatal("zero clock accepted")
	}
}
