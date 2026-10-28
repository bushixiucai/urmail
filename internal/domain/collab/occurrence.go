package collab

import (
	"encoding/json"
	"sort"
	"time"
)

// OccurrenceOverride preserves a recurring series while editing one instance.
// RecurrenceID is the original start, even when the instance is moved.
type OccurrenceOverride struct {
	RecurrenceID time.Time `json:"recurrence_id"`
	Summary      string    `json:"summary"`
	Description  string    `json:"description"`
	Location     string    `json:"location"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
	AllDay       bool      `json:"all_day"`
	Status       string    `json:"status"`
}

func normalizeOverrides(out *Event, in []OccurrenceOverride) error {
	if len(in) > MaxInstances || len(in) > 0 && out.RRule == "" {
		return ErrInvalidEvent
	}
	if in == nil {
		return nil
	}
	out.Overrides = make([]OccurrenceOverride, 0, len(in))
	seen := map[int64]bool{}
	for _, item := range in {
		id := item.RecurrenceID.UTC()
		if id.IsZero() || seen[id.UnixNano()] {
			return ErrInvalidEvent
		}
		seen[id.UnixNano()] = true
		base := *out
		base.Overrides = nil
		base.Status = "confirmed"
		occurrences, err := expandBaseInstances(base, id, id.Add(time.Second))
		if err != nil || len(occurrences) != 1 || !occurrences[0].Equal(id) {
			return ErrInvalidEvent
		}
		child, err := NormalizeEvent(Event{Summary: item.Summary, Description: item.Description, Location: item.Location, StartsAt: item.StartsAt, EndsAt: item.EndsAt, AllDay: item.AllDay, Timezone: out.Timezone, Status: item.Status})
		if err != nil {
			return err
		}
		out.Overrides = append(out.Overrides, OccurrenceOverride{RecurrenceID: id, Summary: child.Summary, Description: child.Description, Location: child.Location, StartsAt: child.StartsAt, EndsAt: child.EndsAt, AllDay: child.AllDay, Status: child.Status})
	}
	sort.Slice(out.Overrides, func(i, j int) bool { return out.Overrides[i].RecurrenceID.Before(out.Overrides[j].RecurrenceID) })
	return nil
}

// InstanceAt returns the data projected for one expanded start.
func InstanceAt(event Event, start time.Time) Event {
	for _, item := range event.Overrides {
		if item.StartsAt.Equal(start) {
			event.Summary = item.Summary
			event.Description = item.Description
			event.Location = item.Location
			event.StartsAt = item.StartsAt
			event.EndsAt = item.EndsAt
			event.AllDay = item.AllDay
			event.Status = item.Status
			event.RecurrenceID = item.RecurrenceID.UTC().Format(time.RFC3339)
			event.Overrides = nil
			event.RRule = ""
			return event
		}
	}
	end := OccurrenceEnd(event, start)
	event.StartsAt = start
	event.EndsAt = end
	event.Overrides = nil
	event.RRule = ""
	return event
}

// DecodeOccurrenceOverrides rejects corrupt stored data at the adapter boundary.
func DecodeOccurrenceOverrides(raw []byte) ([]OccurrenceOverride, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var result []OccurrenceOverride
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, ErrInvalidEvent
	}
	return result, nil
}

// OccurrenceEnd preserves local day boundaries across daylight-saving changes.
func OccurrenceEnd(event Event, start time.Time) time.Time {
	if !event.AllDay {
		return start.Add(event.EndsAt.Sub(event.StartsAt))
	}
	loc, err := time.LoadLocation(event.Timezone)
	if err != nil {
		return start.Add(event.EndsAt.Sub(event.StartsAt))
	}
	a, b := event.StartsAt.In(loc), event.EndsAt.In(loc)
	first := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	last := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	days := int(last.Sub(first) / (24 * time.Hour))
	return start.In(loc).AddDate(0, 0, days).UTC()
}
