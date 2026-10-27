package collab

import (
	"errors"
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
	Summary      string
	Description  string
	Location     string
	StartsAt     time.Time
	EndsAt       time.Time
	AllDay       bool
	Timezone     string
	RRule        string
	Status       string
	RecurrenceID string
	Attendees    []string
	// AttendeeStatus maps an attendee email to its iTIP PARTSTAT value.
	// Entries for unknown attendees or unknown values are dropped; nil means
	// every attendee is still "needs-action".
	AttendeeStatus map[string]string
}

// NormalizeEvent checks bounds and recurrence shape without expanding it.
func NormalizeEvent(in Event) (Event, error) {
	out := Event{
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
	if out.Timezone == "" {
		out.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(out.Timezone); err != nil {
		return Event{}, ErrInvalidEvent
	}
	if out.Summary == "" || !out.EndsAt.After(out.StartsAt) {
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
	if out.RRule != "" && !allowedRRule(out.RRule) {
		return Event{}, ErrInvalidEvent
	}
	if len(in.Attendees) > 0 && len(out.Attendees) != len(in.Attendees) {
		return Event{}, ErrInvalidEvent
	}
	return out, nil
}

func allowedRRule(rule string) bool {
	upper := strings.ToUpper(rule)
	if strings.Contains(upper, "COUNT=") {
		n, err := countValue(upper)
		if err != nil || n < 1 || n > MaxInstances {
			return false
		}
	}
	ok := false
	for _, freq := range []string{"FREQ=DAILY", "FREQ=WEEKLY", "FREQ=MONTHLY", "FREQ=YEARLY"} {
		if strings.Contains(upper, freq) {
			ok = true
		}
	}
	if !ok || strings.Count(rule, ";") >= 8 || !validException(upper) {
		return false
	}
	for _, r := range rule {
		if r == '\r' || r == '\n' {
			return false
		}
	}
	return true
}

func countValue(rule string) (int, error) {
	i := strings.Index(rule, "COUNT=")
	if i < 0 {
		return 0, errors.New("missing")
	}
	rest := rule[i+len("COUNT="):]
	if end := strings.IndexByte(rest, ';'); end >= 0 {
		rest = rest[:end]
	}
	if rest == "" {
		return 0, errors.New("empty")
	}
	v := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			return 0, errors.New("nan")
		}
		v = v*10 + int(r-'0')
	}
	return v, nil
}

func validException(rule string) bool {
	_, value, ok := strings.Cut(rule, "EXDATE=")
	if !ok {
		return true
	}
	value, _, _ = strings.Cut(value, ";")
	if len(value) != 16 || !strings.HasSuffix(value, "Z") {
		return false
	}
	_, err := time.Parse("20060102T150405Z", value)
	return err == nil
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
