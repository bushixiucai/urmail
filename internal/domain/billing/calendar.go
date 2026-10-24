package billing

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidCalendarAnchor rejects missing timezone or invalid wall-clock state.
var ErrInvalidCalendarAnchor = errors.New("invalid billing calendar anchor")

// CalendarAnchor freezes the first paid instant's tenant-local day and wall
// clock. Persist it with the subscription; never derive it from a clamped end
// or from a subsequently changed tenant timezone.
type CalendarAnchor struct {
	Timezone   string `json:"timezone"`
	Day        int    `json:"day"`
	Hour       int    `json:"hour"`
	Minute     int    `json:"minute"`
	Second     int    `json:"second"`
	Nanosecond int    `json:"nanosecond"`
}

// NewCalendarAnchor captures the first payment in the then-current business zone.
func NewCalendarAnchor(firstPaidAt time.Time, timezone string) (CalendarAnchor, error) {
	if firstPaidAt.IsZero() {
		return CalendarAnchor{}, ErrInvalidCalendarAnchor
	}
	location, err := calendarLocation(timezone)
	if err != nil {
		return CalendarAnchor{}, err
	}
	local := firstPaidAt.In(location)
	return CalendarAnchor{Timezone: timezone, Day: local.Day(), Hour: local.Hour(), Minute: local.Minute(), Second: local.Second(), Nanosecond: local.Nanosecond()}, nil
}

func calendarLocation(timezone string) (*time.Location, error) {
	if timezone == "" || timezone == "Local" {
		return nil, ErrInvalidCalendarAnchor
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("%w: timezone", ErrInvalidCalendarAnchor)
	}
	return location, nil
}

func (anchor CalendarAnchor) location() (*time.Location, error) {
	if anchor.Day < 1 || anchor.Day > 31 || anchor.Hour < 0 || anchor.Hour > 23 || anchor.Minute < 0 || anchor.Minute > 59 || anchor.Second < 0 || anchor.Second > 59 || anchor.Nanosecond < 0 || anchor.Nanosecond >= int(time.Second) {
		return nil, ErrInvalidCalendarAnchor
	}
	return calendarLocation(anchor.Timezone)
}

// AddAnchoredCycle advances from the actual start's local calendar month. It
// clamps only the target month, restoring the original day in later periods.
// Overlapping wall times choose the earlier instant. Missing wall times shift
// forward by the gap; later periods restore the original wall-clock anchor.
func AddAnchoredCycle(currentStart time.Time, cycle string, anchor CalendarAnchor) (time.Time, error) {
	location, err := anchor.location()
	if err != nil {
		return time.Time{}, err
	}
	if currentStart.IsZero() {
		return time.Time{}, ErrInvalidPeriod
	}
	months := 0
	switch cycle {
	case CycleMonthly:
		months = 1
	case CycleQuarterly:
		months = 3
	case CycleAnnual, "yearly":
		months = 12
	default:
		return time.Time{}, fmt.Errorf("%w: %s", ErrInvalidCycle, cycle)
	}
	local := currentStart.In(location)
	target := time.Date(local.Year(), local.Month()+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
	last := time.Date(target.Year(), target.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	wall := time.Date(target.Year(), target.Month(), min(anchor.Day, last), anchor.Hour, anchor.Minute, anchor.Second, anchor.Nanosecond, time.UTC)
	end := resolveCalendarWall(wall, location)
	if !end.After(currentStart) {
		return time.Time{}, ErrInvalidPeriod
	}
	return end, nil
}

// resolveCalendarWall enumerates actual timezone intervals around the target,
// rather than trusting time.Date's unspecified DST ambiguity choice. UTC is
// used here only as a sortable representation of local wall-clock components.
func resolveCalendarWall(wall time.Time, location *time.Location) time.Time {
	offsets := map[int]bool{}
	cursor, limit := wall.Add(-72*time.Hour), wall.Add(72*time.Hour)
	for !cursor.After(limit) {
		local := cursor.In(location)
		_, offset := local.Zone()
		offsets[offset] = true
		_, until := local.ZoneBounds()
		if until.IsZero() || !until.After(cursor) {
			break
		}
		cursor = until
	}
	var exact, after time.Time
	var afterWall time.Time
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second).In(location)
		actual := time.Date(candidate.Year(), candidate.Month(), candidate.Day(), candidate.Hour(), candidate.Minute(), candidate.Second(), candidate.Nanosecond(), time.UTC)
		if actual.Equal(wall) {
			if exact.IsZero() || candidate.Before(exact) {
				exact = candidate
			}
		}
		if actual.After(wall) && (after.IsZero() || actual.Before(afterWall) || (actual.Equal(afterWall) && candidate.Before(after))) {
			after = candidate
			afterWall = actual
		}
	}
	if !exact.IsZero() {
		return exact
	}
	return after
}

// CalculateAnchoredRenewalPeriod preserves prepaid time and the frozen anchor.
// A lapse starts a new period at now without changing the original anchor.
// Callers must supply a new date anchor after interrupted paid service; this
// helper never guesses whether a persisted calendar should be reactivated.
func CalculateAnchoredRenewalPeriod(now, currentPeriodEnd time.Time, cycle string, anchor CalendarAnchor) (time.Time, time.Time, error) {
	if now.IsZero() {
		return time.Time{}, time.Time{}, ErrInvalidPeriod
	}
	start := now
	if currentPeriodEnd.After(now) {
		start = currentPeriodEnd
	}
	end, err := AddAnchoredCycle(start, cycle, anchor)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, end, nil
}
