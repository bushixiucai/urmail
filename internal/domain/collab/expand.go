package collab

import (
	"sort"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// ExpandInstances returns bounded occurrences inside the window.
// Weekly, monthly, and yearly rules use the start date's weekday or day.
// Single-occurrence overrides preserve the original series identity.
func ExpandInstances(event Event, windowStart, windowEnd time.Time) ([]time.Time, error) {
	norm, err := NormalizeEvent(event)
	if err != nil {
		return nil, err
	}
	if norm.Status == statusCancelled {
		return nil, nil
	}
	base := norm
	base.Overrides = nil
	starts, err := expandBaseInstances(base, windowStart, windowEnd)
	if err != nil {
		return nil, err
	}
	replaced := map[int64]bool{}
	for _, override := range norm.Overrides {
		replaced[override.RecurrenceID.UnixNano()] = true
	}
	result := make([]time.Time, 0, len(starts)+len(norm.Overrides))
	for _, start := range starts {
		if !replaced[start.UnixNano()] {
			result = append(result, start)
		}
	}
	for _, override := range norm.Overrides {
		if override.Status != statusCancelled && overlaps(override.StartsAt, override.EndsAt, windowStart, windowEnd) {
			result = append(result, override.StartsAt)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Before(result[j]) })
	if len(result) > MaxInstances {
		return nil, ErrInvalidEvent
	}
	return result, nil
}

func expandBaseInstances(event Event, windowStart, windowEnd time.Time) ([]time.Time, error) {
	norm, err := NormalizeEvent(event)
	if err != nil {
		return nil, err
	}
	if !windowEnd.After(windowStart) {
		return nil, ErrInvalidEvent
	}
	if norm.Status == statusCancelled {
		return nil, nil
	}
	if norm.RRule == "" {
		if overlaps(norm.StartsAt, norm.EndsAt, windowStart, windowEnd) {
			return []time.Time{norm.StartsAt}, nil
		}
		return nil, nil
	}
	loc, err := time.LoadLocation(norm.Timezone)
	if err != nil {
		return nil, ErrInvalidEvent
	}
	base, exception := splitException(norm.RRule)
	options, err := rrule.StrToROptionInLocation(base, loc)
	if err != nil {
		return nil, ErrInvalidEvent
	}
	options.Dtstart = norm.StartsAt.In(loc)
	rule, err := rrule.NewRRule(*options)
	if err != nil {
		return nil, ErrInvalidEvent
	}
	next := rule.Iterator()
	out := []time.Time{}
	exceptions := map[string]bool{}
	if exception != "" {
		for _, v := range strings.Split(exception, ",") {
			exceptions[v] = true
		}
	}
	remainingExceptions := len(exceptions)
	for scanned := 0; scanned < 20000; scanned++ {
		occurrence, ok := next()
		if !ok {
			if remainingExceptions != 0 {
				return nil, ErrInvalidEvent
			}
			return out, nil
		}
		occurrence = occurrence.UTC()
		if stamp := occurrence.Format("20060102T150405Z"); exceptions[stamp] {
			delete(exceptions, stamp)
			remainingExceptions--
			continue
		}
		if !occurrence.Before(windowEnd) && remainingExceptions == 0 {
			return out, nil
		}
		if overlaps(occurrence, OccurrenceEnd(norm, occurrence), windowStart, windowEnd) {
			out = append(out, occurrence)
			if len(out) > MaxInstances {
				return nil, ErrInvalidEvent
			}
		}
	}
	return nil, ErrInvalidEvent
}

func overlaps(start, end, windowStart, windowEnd time.Time) bool {
	return start.Before(windowEnd) && end.After(windowStart)
}
