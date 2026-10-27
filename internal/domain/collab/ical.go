// Package collab implements domain rules for collab.
package collab

import (
	"strings"
	"time"
)

// ICalendar renders one event as a minimal iCalendar object.
func ICalendar(in Event) (string, error) {
	event, err := NormalizeEvent(in)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//urMail//Calendar//EN\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	b.WriteString("DTSTART:" + event.StartsAt.UTC().Format("20060102T150405Z") + "\r\n")
	b.WriteString("DTEND:" + event.EndsAt.UTC().Format("20060102T150405Z") + "\r\n")
	b.WriteString("SUMMARY:" + escapeVCard(event.Summary) + "\r\n")
	if event.RRule != "" {
		rule, exdate := splitException(event.RRule)
		b.WriteString("RRULE:" + rule + "\r\n")
		if exdate != "" {
			b.WriteString("EXDATE:" + exdate + "\r\n")
		}
	}
	for _, attendee := range event.Attendees {
		partstat := strings.ToUpper(PartStatNeedsAction)
		if state, ok := event.AttendeeStatus[attendee]; ok && ValidPartStat(state) {
			partstat = strings.ToUpper(state)
		}
		b.WriteString("ATTENDEE;PARTSTAT=" + partstat + ":mailto:" + attendee + "\r\n")
	}
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return b.String(), nil
}

// ParseICalendar reads exactly one event.
func ParseICalendar(raw string) (Event, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	var event Event
	var calendars, events int
	inEvent := false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.EqualFold(line, "BEGIN:VCALENDAR"):
			calendars++
		case strings.EqualFold(line, "BEGIN:VEVENT"):
			if inEvent {
				return Event{}, ErrInvalidEvent
			}
			inEvent = true
			events++
		case strings.EqualFold(line, "END:VEVENT"):
			inEvent = false
		case strings.HasPrefix(line, "DTSTART:"):
			event.StartsAt = parseStamp(line[len("DTSTART:"):])
		case strings.HasPrefix(line, "DTEND:"):
			event.EndsAt = parseStamp(line[len("DTEND:"):])
		case strings.HasPrefix(line, "SUMMARY:"):
			event.Summary = unescapeVCard(line[len("SUMMARY:"):])
		case strings.HasPrefix(strings.ToUpper(line), "RRULE:"):
			event.RRule = unescapeVCard(line[len("RRULE:"):])
		case strings.HasPrefix(strings.ToUpper(line), "EXDATE:"):
			event.RRule = strings.TrimSuffix(event.RRule, ";") + ";EXDATE=" + unescapeVCard(line[len("EXDATE:"):])
		case strings.HasPrefix(strings.ToUpper(line), "ATTENDEE"):
			email, partstat, ok := parseAttendeeLine(line)
			if !ok {
				continue
			}
			event.Attendees = append(event.Attendees, email)
			if partstat != "" && ValidPartStat(partstat) {
				if event.AttendeeStatus == nil {
					event.AttendeeStatus = map[string]string{}
				}
				event.AttendeeStatus[email] = partstat
			}
		}
	}
	if calendars != 1 || events != 1 {
		return Event{}, ErrInvalidEvent
	}
	return NormalizeEvent(event)
}

func parseStamp(value string) time.Time {
	parsed, err := time.Parse("20060102T150405Z", value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

// Calendar implements the corresponding domain rule.
func Calendar(events []Event) (string, error) {
	if len(events) > 1000 {
		return "", ErrInvalidEvent
	}
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//urMail//Calendar//EN\r\n")
	for _, event := range events {
		one, err := ICalendar(event)
		if err != nil {
			return "", err
		}
		one = strings.TrimPrefix(one, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//urMail//Calendar//EN\r\n")
		one = strings.TrimSuffix(one, "END:VCALENDAR\r\n")
		b.WriteString(one)
	}
	b.WriteString("END:VCALENDAR\r\n")
	return b.String(), nil
}

func splitException(rule string) (string, string) {
	if _, value, ok := strings.Cut(strings.ToUpper(rule), "EXDATE="); ok {
		base := rule
		if i := strings.Index(strings.ToUpper(rule), ";EXDATE="); i >= 0 {
			base = rule[:i]
		}
		return base, value
	}
	return rule, ""
}
