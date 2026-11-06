package handlers

import "time"

// calendarBudgetBounds resolves natural calendar boundaries in a frozen zone.
// A missing midnight advances through the gap; an overlap uses the first
// occurrence. This is the AI quota window, not a billing period.
func calendarBudgetBounds(now time.Time, timezone string) (time.Time, time.Time, time.Time, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil || timezone == "" || timezone == "Local" || now.IsZero() {
		return time.Time{}, time.Time{}, time.Time{}, errAIBudget
	}
	local := now.In(location)
	day := resolveCalendarWall(time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC), location)
	month := resolveCalendarWall(time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, time.UTC), location)
	end := resolveCalendarWall(time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, time.UTC), location)
	if day.IsZero() || month.IsZero() || end.IsZero() || now.Before(day) || now.Before(month) || !now.Before(end) {
		return time.Time{}, time.Time{}, time.Time{}, errAIBudget
	}
	return day, month, end, nil
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
