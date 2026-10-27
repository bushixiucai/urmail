package collab

import (
	"strings"
	"time"
)

// ExpandInstances returns bounded occurrences inside the window.
// Weekly, monthly, and yearly rules use the start date's weekday or day.
// Exceptions and attendees are not represented.
func ExpandInstances(event Event, windowStart, windowEnd time.Time) ([]time.Time, error) {
	norm, err := NormalizeEvent(event)
	if err != nil {
		return nil, err
	}
	if !windowEnd.After(windowStart) {
		return nil, ErrInvalidEvent
	}
	if norm.RRule == "" {
		if overlaps(norm.StartsAt, norm.EndsAt, windowStart, windowEnd) {
			return []time.Time{norm.StartsAt}, nil
		}
		return nil, nil
	}
	upper := strings.ToUpper(norm.RRule)
	count := MaxInstances
	if strings.Contains(upper, "COUNT=") {
		n, err := countValue(upper)
		if err != nil {
			return nil, ErrInvalidEvent
		}
		count = n
	}
	step := recurrenceStep(upper)
	if step == "" {
		return nil, ErrInvalidEvent
	}
	var out []time.Time
	start := norm.StartsAt
	for i := 0; i < count && len(out) < MaxInstances; i++ {
		occ := advance(start, step, i)
		end := occ.Add(norm.EndsAt.Sub(norm.StartsAt))
		if end.After(windowEnd) && occ.After(windowEnd) {
			break
		}
		if excluded(norm.RRule, occ) {
			continue
		}
		if overlaps(occ, end, windowStart, windowEnd) {
			out = append(out, occ)
		}
	}
	if exception, ok := exceptionTime(norm.RRule); ok {
		matched := false
		for i := 0; i < count && i < MaxInstances; i++ {
			if advance(start, step, i).UTC().Equal(exception) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, ErrInvalidEvent
		}
	}
	return out, nil
}

func recurrenceStep(rule string) string {
	switch {
	case strings.Contains(rule, "FREQ=DAILY"):
		return "daily"
	case strings.Contains(rule, "FREQ=WEEKLY"):
		return "weekly"
	case strings.Contains(rule, "FREQ=MONTHLY"):
		return "monthly"
	case strings.Contains(rule, "FREQ=YEARLY"):
		return "yearly"
	default:
		return ""
	}
}

func advance(start time.Time, step string, n int) time.Time {
	switch step {
	case "daily":
		return start.AddDate(0, 0, n)
	case "weekly":
		return start.AddDate(0, 0, 7*n)
	case "monthly":
		return start.AddDate(0, n, 0)
	case "yearly":
		return start.AddDate(n, 0, 0)
	default:
		return start
	}
}

func overlaps(start, end, windowStart, windowEnd time.Time) bool {
	return start.Before(windowEnd) && end.After(windowStart)
}

func excluded(rule string, occ time.Time) bool {
	_, value, ok := strings.Cut(strings.ToUpper(rule), "EXDATE=")
	if !ok {
		return false
	}
	value, _, _ = strings.Cut(value, ";")
	return strings.Contains(value, occ.UTC().Format("20060102T150405Z"))
}

func exceptionTime(rule string) (time.Time, bool) {
	_, value, ok := strings.Cut(strings.ToUpper(rule), "EXDATE=")
	if !ok {
		return time.Time{}, false
	}
	value, _, _ = strings.Cut(value, ";")
	parsed, err := time.Parse("20060102T150405Z", value)
	return parsed, err == nil
}
