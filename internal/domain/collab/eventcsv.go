// Package collab implements domain rules for collab.
package collab

import (
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"time"
)

// ParseEventsCSV implements the corresponding domain rule.
func ParseEventsCSV(raw string) ([]Event, error) {
	reader := csv.NewReader(strings.NewReader(raw))
	reader.FieldsPerRecord = -1
	var out []Event
	first := true
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || len(row) < 3 || len(out) == 1000 {
			return nil, ErrInvalidEvent
		}
		if first && strings.EqualFold(row[0], "summary") {
			first = false
			continue
		}
		first = false
		start, startErr := time.Parse(time.RFC3339, row[1])
		end, endErr := time.Parse(time.RFC3339, row[2])
		if startErr != nil || endErr != nil {
			return nil, ErrInvalidEvent
		}
		rule := ""
		if len(row) >= 4 {
			rule = row[3]
		}
		event, err := NormalizeEvent(Event{Summary: row[0], StartsAt: start, EndsAt: end, Timezone: "UTC", RRule: rule})
		if err == nil && rule != "" {
			if _, err = ExpandInstances(event, start, start.AddDate(5, 0, 0)); err != nil {
				return nil, err
			}
		}
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	if len(out) == 0 {
		return nil, ErrInvalidEvent
	}
	return out, nil
}
