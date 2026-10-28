package collab

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrInvalidEvent is returned when an event cannot be stored.
var ErrInvalidEvent = errors.New("invalid event")

// MaxInstances bounds recurrence expansion so a rule cannot stall a request.
const MaxInstances = 400

// statusCancelled holds the RFC 5545 STATUS property value used when an
// event is no longer active (the value is the RFC's own spelling).
const statusCancelled = "cancelled" //nolint:misspell // RFC 5545 spelling

// Event is one calendar entry.
type Event struct {
	Organizer       string
	Sequence        int
	UID             string
	ReminderMinutes []int
	Overrides       []OccurrenceOverride
	Summary         string
	Description     string
	Location        string
	StartsAt        time.Time
	EndsAt          time.Time
	AllDay          bool
	Timezone        string
	RRule           string
	Status          string
	RecurrenceID    string
	Attendees       []string
	// AttendeeStatus maps an attendee email to its iTIP PARTSTAT value.
	// Entries for unknown attendees or unknown values are dropped; nil means
	// every attendee is still "needs-action".
	AttendeeStatus map[string]string
}

// NormalizeEvent checks bounds and recurrence shape without expanding it.
func NormalizeEvent(in Event) (Event, error) {
	if utf8.RuneCountInString(in.UID) > 255 || strings.ContainsAny(in.UID, "\r\n") || utf8.RuneCountInString(in.Summary) > 500 || utf8.RuneCountInString(in.Location) > 500 {
		return Event{}, ErrInvalidEvent
	}

	out := Event{
		UID: cleanLine(in.UID, 255), Organizer: strings.ToLower(strings.TrimSpace(in.Organizer)), Sequence: in.Sequence,
		Summary:        cleanLine(in.Summary, 500),
		Description:    strings.TrimSpace(in.Description),
		Location:       cleanLine(in.Location, 500),
		StartsAt:       in.StartsAt.UTC(),
		EndsAt:         in.EndsAt.UTC(),
		AllDay:         in.AllDay,
		Timezone:       cleanLine(in.Timezone, 100),
		RRule:          cleanLine(in.RRule, 500),
		Status:         strings.ToLower(cleanLine(in.Status, 20)),
		RecurrenceID:   cleanLine(in.RecurrenceID, 40),
		Attendees:      cleanAttendees(in.Attendees),
		AttendeeStatus: cleanAttendeeStatus(in.AttendeeStatus, cleanAttendees(in.Attendees)),
	}
	if out.Sequence < 0 || out.Sequence > 2147483647 || (out.Organizer != "" && !validAttendee(out.Organizer)) {
		return Event{}, ErrInvalidEvent
	}
	if out.Timezone == "" {
		out.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(out.Timezone); err != nil {
		return Event{}, ErrInvalidEvent
	}
	if out.Summary == "" || out.StartsAt.IsZero() || out.EndsAt.IsZero() || !out.EndsAt.After(out.StartsAt) {
		return Event{}, ErrInvalidEvent
	}
	if utf8.RuneCountInString(out.Description) > 8000 {
		return Event{}, ErrInvalidEvent
	}
	if out.Status == "" {
		out.Status = "confirmed"
	}
	switch out.Status {
	case "confirmed", "tentative", statusCancelled: //nolint:misspell // RFC 5545 STATUS value
	default:
		return Event{}, ErrInvalidEvent
	}
	if out.AllDay {
		loc, _ := time.LoadLocation(out.Timezone)
		for _, stamp := range []time.Time{out.StartsAt, out.EndsAt} {
			local := stamp.In(loc)
			if local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 || local.Nanosecond() != 0 {
				return Event{}, ErrInvalidEvent
			}
		}
	}
	if out.RRule != "" && !allowedRRule(out.RRule) {
		return Event{}, ErrInvalidEvent
	}
	if len(in.Attendees) > 0 && len(out.Attendees) != len(in.Attendees) {
		return Event{}, ErrInvalidEvent
	}
	if in.ReminderMinutes != nil {
		if len(in.ReminderMinutes) > 5 {
			return Event{}, ErrInvalidEvent
		}
		out.ReminderMinutes = append([]int{}, in.ReminderMinutes...)
		sort.Ints(out.ReminderMinutes)
		for i, minutes := range out.ReminderMinutes {
			if minutes < 0 || minutes > 40320 || (i > 0 && out.ReminderMinutes[i-1] == minutes) {
				return Event{}, ErrInvalidEvent
			}
		}
	}
	if err := normalizeOverrides(&out, in.Overrides); err != nil {
		return Event{}, err
	}
	return out, nil
}

func allowedRRule(rule string) bool {
	if len(rule) > 500 || strings.ContainsAny(rule, "\r\n") {
		return false
	}
	base, _ := splitException(rule)
	seen := map[string]bool{}
	for _, part := range strings.Split(strings.ToUpper(base), ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok || seen[key] || value == "" {
			return false
		}
		seen[key] = true
		switch key {
		case "FREQ":
			if value != "DAILY" && value != "WEEKLY" && value != "MONTHLY" && value != "YEARLY" {
				return false
			}
		case "COUNT":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > MaxInstances {
				return false
			}
		case "INTERVAL":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 366 {
				return false
			}
		case "UNTIL":
			if _, err := time.Parse("20060102T150405Z", value); err != nil {
				return false
			}
		case "BYDAY", "WKST":
			for _, day := range strings.Split(value, ",") {
				switch day {
				case "MO", "TU", "WE", "TH", "FR", "SA", "SU":
				default:
					return false
				}
			}
		case "BYMONTH":
			for _, v := range strings.Split(value, ",") {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 || n > 12 {
					return false
				}
			}
		case "BYMONTHDAY":
			for _, v := range strings.Split(value, ",") {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 || n > 31 {
					return false
				}
			}
		default:
			return false
		}
	}
	return seen["FREQ"] && validException(strings.ToUpper(rule))
}

func validException(rule string) bool {
	_, raw := splitException(rule)
	if raw == "" {
		return true
	}
	values := strings.Split(raw, ",")
	if len(values) > MaxInstances {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return false
		}
		seen[value] = true
		if _, err := time.Parse("20060102T150405Z", value); err != nil {
			return false
		}
	}
	return true
}

func cleanAttendees(in []string) []string {
	if len(in) > 20 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		email := strings.ToLower(strings.TrimSpace(raw))
		if !validAttendee(email) || seen[email] {
			return nil
		}
		seen[email] = true
		out = append(out, email)
	}
	return out
}

// cleanAttendeeStatus keeps only statuses for known attendees with a known
// PARTSTAT value; anything else is ignored rather than stored.
func cleanAttendeeStatus(in map[string]string, attendees []string) map[string]string {
	if len(in) == 0 || len(attendees) == 0 {
		return nil
	}
	member := make(map[string]bool, len(attendees))
	for _, attendee := range attendees {
		member[attendee] = true
	}
	var out map[string]string
	for email, state := range in {
		email = strings.ToLower(strings.TrimSpace(email))
		if !member[email] || !ValidPartStat(state) {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(in))
		}
		out[email] = state
	}
	return out
}

// AttendeeStates returns every attendee with its stored PARTSTAT, filling
// needs-action for attendees without a stored answer.
func AttendeeStates(event Event) []AttendeeState {
	states := make([]AttendeeState, 0, len(event.Attendees))
	for _, attendee := range event.Attendees {
		state := PartStatNeedsAction
		if stored, ok := event.AttendeeStatus[attendee]; ok && ValidPartStat(stored) {
			state = stored
		}
		states = append(states, AttendeeState{Email: attendee, PartStat: state})
	}
	return states
}

// AttendeeState is one attendee and its stored invitation answer.
type AttendeeState struct {
	Email    string `json:"email"`
	PartStat string `json:"partstat"`
}

func validAttendee(email string) bool {
	name, domain, ok := strings.Cut(email, "@")
	return ok && name != "" && domain != "" && !strings.ContainsAny(email, " \t\r\n,;") && len(email) <= 200
}
