package collab

import (
	"context"
	"strings"
	"time"
)

// iTIP (RFC 5546) methods this implementation generates and accepts.
const (
	MethodRequest = "REQUEST"
	MethodReply   = "REPLY"
	MethodCancel  = "CANCEL"
)

// PartStat values carried on ATTENDEE lines and stored per attendee.
const (
	PartStatNeedsAction = "needs-action"
	PartStatAccepted    = "accepted"
	PartStatDeclined    = "declined"
	PartStatTentative   = "tentative"
)

// ValidPartStat reports whether the value is one this server stores.
func ValidPartStat(value string) bool {
	switch value {
	case PartStatNeedsAction, PartStatAccepted, PartStatDeclined, PartStatTentative:
		return true
	}
	return false
}

// Invitation is one iTIP payload to deliver to attendees.
type Invitation struct {
	// UID identifies the event across updates; Sequence bumps on change.
	UID       string
	Sequence  int
	Method    string // REQUEST or CANCEL
	Organizer string // organizer mailbox address
	Event     Event
	// Stamp is the DTSTAMP source; the zero time means time.Now().
	Stamp time.Time
}

// InviteSender delivers one invitation. Implementations submit real mail;
// the domain refuses to call it without explicit confirmation.
type InviteSender func(context.Context, Invitation) error

// InvitationICS renders the iTIP object for the invitation method.
func InvitationICS(inv Invitation) (string, error) {
	if inv.UID == "" || !validAttendee(inv.Organizer) {
		return "", ErrInvalidEvent
	}
	if inv.Method != MethodRequest && inv.Method != MethodCancel {
		return "", ErrInvalidEvent
	}
	if inv.Method == MethodCancel && inv.Event.Status != statusCancelled {
		return "", ErrInvalidEvent
	}
	if inv.Method == MethodRequest && inv.Event.Status == statusCancelled {
		return "", ErrInvalidEvent
	}
	event, err := NormalizeEvent(inv.Event)
	if err != nil {
		return "", err
	}
	if len(event.Attendees) == 0 {
		return "", ErrInvalidEvent
	}
	stamp := inv.Stamp
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	event.UID = inv.UID
	event.Organizer = inv.Organizer
	event.Sequence = inv.Sequence
	if inv.Method == MethodCancel {
		event.AttendeeStatus = nil
		for i := range event.Overrides {
			event.Overrides[i].Status = statusCancelled
		}
	}
	body, err := ICalendar(event)
	if err != nil {
		return "", err
	}
	body = strings.Replace(body, "PRODID:-//urMail//Calendar//EN\r\n", "PRODID:-//urMail//Calendar//EN\r\nMETHOD:"+inv.Method+"\r\n", 1)

	// Re-fold after adding transport properties; no client-supplied raw headers.
	unfolded := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n ", ""), "\r\n\t", "")
	var result strings.Builder
	for _, line := range strings.Split(unfolded, "\r\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "DTSTAMP:") {
			line = "DTSTAMP:" + stamp.UTC().Format("20060102T150405Z")
		}
		if inv.Method == MethodRequest && strings.HasPrefix(line, "ATTENDEE;") {
			line = strings.Replace(line, ":mailto:", ";RSVP=TRUE:mailto:", 1)
		}
		result.WriteString(foldICal(line))
		result.WriteString("\r\n")
	}
	return result.String(), nil
}

// Reply is one attendee's answer to an invitation (METHOD:REPLY).
type Reply struct {
	UID      string
	Replier  string
	PartStat string
}

// ParseReply reads one METHOD:REPLY object and extracts the replying
// attendee. RFC 5546 requires exactly one ATTENDEE on a REPLY; more than one
// is refused so a replayed or forged payload cannot set someone else's
// status. The UID must be present so the caller can bind the reply to a
// single stored event.
func ParseReply(raw string) (Reply, error) {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n ", ""), "\r\n\t", "")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	var reply Reply
	method := ""
	var attendees []string
	partstats := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "METHOD:"):
			method = line[len("METHOD:"):]
		case strings.HasPrefix(upper, "UID:"):
			reply.UID = unescapeVCard(line[len("UID:"):])
		case strings.HasPrefix(upper, "ATTENDEE"):
			email, partstat, ok := parseAttendeeLine(line)
			if !ok {
				return Reply{}, ErrInvalidEvent
			}
			attendees = append(attendees, email)
			partstats[email] = partstat
		}
	}
	if !strings.EqualFold(method, MethodReply) || len(attendees) != 1 || reply.UID == "" {
		return Reply{}, ErrInvalidEvent
	}
	email := attendees[0]
	partstat := partstats[email]
	if partstat == "" {
		partstat = PartStatNeedsAction
	}
	if !ValidPartStat(partstat) {
		return Reply{}, ErrInvalidEvent
	}
	return Reply{UID: reply.UID, Replier: email, PartStat: partstat}, nil
}

// parseAttendeeLine splits "ATTENDEE;PARAM=VALUE:mailto:addr".
func parseAttendeeLine(line string) (email, partstat string, ok bool) {
	params, value, found := cutICalProperty(line)
	if !found {
		return "", "", false
	}
	for _, param := range splitICalParams(params)[1:] {
		key, value, ok := strings.Cut(param, "=")
		if ok && strings.EqualFold(key, "PARTSTAT") {
			if partstat != "" {
				return "", "", false
			}
			partstat = strings.ToLower(strings.Trim(value, "\""))
		}
	}
	if !strings.HasPrefix(strings.ToUpper(value), "MAILTO:") {
		return "", "", false
	}
	email = strings.ToLower(strings.TrimSpace(value[len("MAILTO:"):]))
	if !validAttendee(email) {
		return "", "", false
	}
	return email, partstat, true
}

// ApplyReply records one attendee's own answer. The replier must already be
// an attendee of the event: unknown addresses are refused, so a reply can
// never forge another participant's status. Other attendees keep their
// stored status.
func ApplyReply(event Event, reply Reply) (Event, error) {
	if !ValidPartStat(reply.PartStat) {
		return Event{}, ErrInvalidEvent
	}
	replier := strings.ToLower(strings.TrimSpace(reply.Replier))
	if !validAttendee(replier) {
		return Event{}, ErrInvalidEvent
	}
	matched := false
	for _, attendee := range event.Attendees {
		if attendee == replier {
			matched = true
			break
		}
	}
	if !matched {
		return Event{}, ErrInvalidEvent
	}
	next := Event{
		Summary: event.Summary, Description: event.Description, Location: event.Location,
		StartsAt: event.StartsAt, EndsAt: event.EndsAt, AllDay: event.AllDay,
		Timezone: event.Timezone, RRule: event.RRule, Status: event.Status,
		RecurrenceID: event.RecurrenceID, Attendees: event.Attendees,
		AttendeeStatus: make(map[string]string, len(event.Attendees)),
	}
	for attendee, state := range event.AttendeeStatus {
		next.AttendeeStatus[attendee] = state
	}
	next.AttendeeStatus[replier] = reply.PartStat
	return NormalizeEvent(next)
}
