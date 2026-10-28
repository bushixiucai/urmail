package collab

import (
	"strconv"
	"strings"
	"time"
)

// SchedulingMessage is a bounded iTIP request, response or cancellation. The
// transport must authenticate its organizer/replier separately before writes.
type SchedulingMessage struct {
	Method, UID, Organizer string
	Sequence               int
	Stamp                  time.Time
	RecurrenceID           string
	RecurrenceIsDate       bool
	RecurrenceDate         string
	Event                  *Event
	Reply                  *Reply
}

// ParseSchedulingMessage accepts one UID and organizer. Single-instance
// REQUEST/CANCEL messages retain their recurrence identity rather than being
// silently applied to the entire series.
func ParseSchedulingMessage(raw string) (SchedulingMessage, error) {
	var out SchedulingMessage
	if len(raw) > 200000 {
		return out, ErrInvalidEvent
	}
	unfolded := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n ", ""), "\r\n\t", ""), "\r\n", "\n")
	var blocks [][]string
	var block []string
	inside := false
	calendars := 0
	methods := 0
	for _, line := range strings.Split(unfolded, "\n") {
		switch strings.ToUpper(line) {
		case "BEGIN:VCALENDAR":
			calendars++
		case "BEGIN:VEVENT":
			if inside {
				return out, ErrInvalidEvent
			}
			inside = true
			block = nil
		case "END:VEVENT":
			if !inside {
				return out, ErrInvalidEvent
			}
			blocks = append(blocks, block)
			inside = false
		default:
			if strings.HasPrefix(strings.ToUpper(line), "METHOD:") {
				methods++
				_, method, _ := cutICalProperty(line)
				out.Method = strings.ToUpper(method)
			}
			if inside {
				block = append(block, line)
			}
		}
	}
	if calendars != 1 || methods != 1 || inside || len(blocks) == 0 || len(blocks) > 100 {
		return out, ErrInvalidEvent
	}
	if out.Method != MethodRequest && out.Method != MethodReply && out.Method != MethodCancel {
		return out, ErrInvalidEvent
	}
	for i, lines := range blocks {
		uid, organizer, recurrence := "", "", ""
		recurrenceIsDate := false
		recurrenceDate := ""
		sequence := 0
		var stamp time.Time
		properties := map[string]bool{}
		alarmDepth := 0
		for _, line := range lines {
			if strings.EqualFold(line, "BEGIN:VALARM") {
				alarmDepth++
				continue
			}
			if strings.EqualFold(line, "END:VALARM") {
				alarmDepth--
				continue
			}
			if alarmDepth != 0 {
				continue
			}
			name, value, ok := cutICalProperty(line)
			if !ok {
				continue
			}
			key := strings.ToUpper(strings.Split(name, ";")[0])
			switch key {
			case "UID", "ORGANIZER", "SEQUENCE", "DTSTAMP", "RECURRENCE-ID":
				if properties[key] {
					return out, ErrInvalidEvent
				}
				properties[key] = true
			}
			switch key {
			case "UID":
				uid = unescapeVCard(value)
			case "ORGANIZER":
				scheme, address, ok := strings.Cut(value, ":")
				if !ok || !strings.EqualFold(scheme, "mailto") {
					return out, ErrInvalidEvent
				}
				organizer = strings.ToLower(address)
			case "SEQUENCE":
				n, e := strconv.ParseInt(value, 10, 32)
				if e != nil || n < 0 {
					return out, ErrInvalidEvent
				}
				sequence = int(n)
			case "DTSTAMP":
				t, e := time.Parse("20060102T150405Z", value)
				if e != nil {
					return out, ErrInvalidEvent
				}
				stamp = t
			case "RECURRENCE-ID":
				// Reuse the shared timezone/date parser without requiring an event body.
				stamp, _, allDay, e := parseICalStamp(name, value, "UTC")
				if e != nil {
					return out, ErrInvalidEvent
				}
				recurrence = stamp.UTC().Format(time.RFC3339)
				recurrenceIsDate = allDay
				if allDay {
					recurrenceDate = value
				}
			}
		}
		if uid == "" || len(uid) > 255 || !validAttendee(organizer) || stamp.IsZero() {
			return out, ErrInvalidEvent
		}
		if i == 0 {
			out.UID = uid
			out.Organizer = organizer
			out.Sequence = sequence
			out.Stamp = stamp
			out.RecurrenceID = recurrence
			out.RecurrenceIsDate = recurrenceIsDate
			out.RecurrenceDate = recurrenceDate
		} else if uid != out.UID || organizer != out.Organizer || sequence != out.Sequence || !stamp.Equal(out.Stamp) {
			return out, ErrInvalidEvent
		}
	}
	switch out.Method {
	case MethodRequest:
		var event Event
		var err error
		if len(blocks) == 1 && out.RecurrenceID != "" {
			event, err = parseEventLines(blocks[0])
			if err == nil {
				event, err = NormalizeEvent(event)
			}
		} else {
			event, err = ParseICalendar(raw)
		}
		if err != nil {
			return out, err
		}
		if event.Status == statusCancelled {
			return out, ErrInvalidEvent
		}
		out.Event = &event
	case MethodReply:
		if len(blocks) != 1 {
			return out, ErrInvalidEvent
		}
		reply, err := ParseReply(raw)
		if err != nil {
			return out, err
		}
		out.Reply = &reply
	default:
		if len(blocks) > 1 && out.RecurrenceID != "" {
			return out, ErrInvalidEvent
		}
	}
	return out, nil
}

func parseICalStamp(name, value, defaultZone string) (time.Time, string, bool, error) {
	zone := defaultZone
	allDay := false
	for _, param := range strings.Split(name, ";")[1:] {
		key, v, _ := strings.Cut(param, "=")
		if strings.EqualFold(key, "TZID") {
			zone = strings.Trim(v, "\"")
		}
		if strings.EqualFold(param, "VALUE=DATE") {
			allDay = true
		}
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, "", false, ErrInvalidEvent
	}
	var stamp time.Time
	switch {
	case len(value) == 8:
		stamp, err = time.ParseInLocation("20060102", value, location)
		allDay = true
	case strings.HasSuffix(value, "Z"):
		stamp, err = time.Parse("20060102T150405Z", value)
	default:
		stamp, err = time.ParseInLocation("20060102T150405", value, location)
	}
	if err != nil {
		return time.Time{}, "", false, ErrInvalidEvent
	}
	return stamp, zone, allDay, nil
}

// ResponseICS emits exactly one attendee response with the organizer's UID,
// sequence and optional occurrence identity; it never increments the sequence.
func ResponseICS(event Event, organizer, replier, status string, sequence int, stamp time.Time) (string, error) {
	if !validAttendee(organizer) || !validAttendee(replier) || !ValidPartStat(status) || status == PartStatNeedsAction {
		return "", ErrInvalidEvent
	}
	event.Organizer = organizer
	event.Sequence = sequence
	event.Attendees = []string{replier}
	event.AttendeeStatus = map[string]string{replier: status}
	event.ReminderMinutes = nil
	event.RRule = ""
	event.Overrides = nil
	normalized, err := NormalizeEvent(event)
	if err != nil {
		return "", err
	}
	recurrence := ""
	if event.RecurrenceID != "" {
		parsed, e := time.Parse(time.RFC3339, event.RecurrenceID)
		if e != nil {
			return "", e
		}
		recurrence = parsed.UTC().Format("20060102T150405Z")
	}
	var b strings.Builder
	b.WriteString(calendarHeader)
	b.WriteString("METHOD:REPLY\r\n")
	writeICalEvent(&b, normalized, recurrence, event.AllDay)
	b.WriteString("END:VCALENDAR\r\n")
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	body := strings.Replace(b.String(), "DTSTAMP:"+normalized.StartsAt.UTC().Format("20060102T150405Z"), "DTSTAMP:"+stamp.UTC().Format("20060102T150405Z"), 1)
	return body, nil
}
