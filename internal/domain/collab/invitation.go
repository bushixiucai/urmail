package collab

import (
	"context"
	"fmt"
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
	status := "CONFIRMED"
	switch event.Status {
	case statusCancelled:
		status = "CANCELLED" //nolint:misspell // RFC 5545 spelling
	case "tentative":
		status = "TENTATIVE"
	}
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//urMail//Calendar//EN\r\n")
	b.WriteString("METHOD:" + inv.Method + "\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	b.WriteString("UID:" + escapeVCard(inv.UID) + "\r\n")
	b.WriteString("SEQUENCE:" + fmt.Sprint(inv.Sequence) + "\r\n")
	b.WriteString("DTSTAMP:" + stamp.UTC().Format("20060102T150405Z") + "\r\n")
	b.WriteString("DTSTART:" + event.StartsAt.UTC().Format("20060102T150405Z") + "\r\n")
	b.WriteString("DTEND:" + event.EndsAt.UTC().Format("20060102T150405Z") + "\r\n")
	b.WriteString("SUMMARY:" + escapeVCard(event.Summary) + "\r\n")
	if event.Location != "" {
		b.WriteString("LOCATION:" + escapeVCard(event.Location) + "\r\n")
	}
	if event.Description != "" {
		b.WriteString("DESCRIPTION:" + escapeVCard(event.Description) + "\r\n")
	}
	b.WriteString("STATUS:" + status + "\r\n")
	if inv.Method == MethodCancel {
		for _, attendee := range event.Attendees {
			b.WriteString("ATTENDEE;PARTSTAT=" + strings.ToUpper(PartStatNeedsAction) + ":mailto:" + attendee + "\r\n")
		}
	} else {
		for _, attendee := range event.Attendees {
			partstat := strings.ToUpper(PartStatNeedsAction)
			if state, ok := event.AttendeeStatus[attendee]; ok && ValidPartStat(state) {
				partstat = strings.ToUpper(state)
			}
			b.WriteString("ATTENDEE;PARTSTAT=" + partstat + ";RSVP=TRUE:mailto:" + attendee + "\r\n")
		}
	}
	b.WriteString("ORGANIZER:mailto:" + inv.Organizer + "\r\n")
	b.WriteString("END:VEVENT\r\n")
	b.WriteString("END:VCALENDAR\r\n")
	return b.String(), nil
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
	rest := line[len("ATTENDEE"):]
	idx := strings.Index(rest, ":")
	if idx < 0 {
		return "", "", false
	}
	params, value := rest[:idx], rest[idx+1:]
	if _, val, found := strings.Cut(strings.ToUpper(params), "PARTSTAT="); found {
		if end := strings.IndexByte(val, ';'); end >= 0 {
			val = val[:end]
		}
		partstat = strings.ToLower(strings.Trim(val, "\""))
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
