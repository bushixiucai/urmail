package collab

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const calendarHeader = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//urMail//Calendar//EN\r\n"

// ICalendar renders a series and its exceptions in the same RFC 5545 resource.
func ICalendar(in Event) (string, error) {
	event, err := NormalizeEvent(in)
	if err != nil {
		return "", err
	}
	if event.UID == "" {
		sum := sha256.Sum256([]byte(event.Summary + event.StartsAt.Format(time.RFC3339Nano) + event.EndsAt.Format(time.RFC3339Nano)))
		event.UID = hex.EncodeToString(sum[:16]) + "@urmail"
	}
	var b strings.Builder
	b.WriteString(calendarHeader)
	writeICalEvent(&b, event, "", false)
	for _, override := range event.Overrides {
		child := InstanceAt(event, override.StartsAt)
		// A canceled instance still needs a component, although it does not expand.
		child.Status = override.Status
		writeICalEvent(&b, child, override.RecurrenceID.UTC().Format("20060102T150405Z"), event.AllDay)
	}
	b.WriteString("END:VCALENDAR\r\n")
	return b.String(), nil
}

func writeICalEvent(b *strings.Builder, event Event, recurrenceID string, recurrenceAllDay bool) {
	line := func(value string) { b.WriteString(foldICal(value)); b.WriteString("\r\n") }
	line("BEGIN:VEVENT")
	line("UID:" + escapeVCard(event.UID))
	if event.Organizer != "" {
		line("SEQUENCE:" + strconv.Itoa(event.Sequence))
	}
	line("DTSTAMP:" + event.StartsAt.UTC().Format("20060102T150405Z"))
	if recurrenceID != "" {
		if recurrenceAllDay {
			stamp, _ := time.Parse("20060102T150405Z", recurrenceID)
			loc, _ := time.LoadLocation(event.Timezone)
			line("RECURRENCE-ID;VALUE=DATE:" + stamp.In(loc).Format("20060102"))
		} else {
			line("RECURRENCE-ID:" + recurrenceID)
		}
	}
	if event.AllDay {
		loc, _ := time.LoadLocation(event.Timezone)
		if loc == nil {
			loc = time.UTC
		}
		line("DTSTART;VALUE=DATE:" + event.StartsAt.In(loc).Format("20060102"))
		line("DTEND;VALUE=DATE:" + event.EndsAt.In(loc).Format("20060102"))
	} else {
		line("DTSTART:" + event.StartsAt.UTC().Format("20060102T150405Z"))
		line("DTEND:" + event.EndsAt.UTC().Format("20060102T150405Z"))
	}
	if event.Timezone != "UTC" {
		line("X-WR-TIMEZONE:" + event.Timezone)
	}
	line("SUMMARY:" + escapeVCard(event.Summary))
	if event.Description != "" {
		line("DESCRIPTION:" + escapeVCard(event.Description))
	}
	if event.Location != "" {
		line("LOCATION:" + escapeVCard(event.Location))
	}
	line("STATUS:" + strings.ToUpper(event.Status))
	if event.RRule != "" {
		rule, exdate := splitException(event.RRule)
		line("RRULE:" + rule)
		if exdate != "" {
			line("EXDATE:" + exdate)
		}
	}
	for _, attendee := range event.Attendees {
		partstat := PartStatNeedsAction
		if state, ok := event.AttendeeStatus[attendee]; ok && ValidPartStat(state) {
			partstat = state
		}
		line("ATTENDEE;PARTSTAT=" + strings.ToUpper(partstat) + ":mailto:" + attendee)
	}
	for _, minutes := range event.ReminderMinutes {
		line("BEGIN:VALARM")
		line("TRIGGER:-PT" + strconv.Itoa(minutes) + "M")
		line("ACTION:DISPLAY")
		line("DESCRIPTION:" + escapeVCard(event.Summary))
		line("END:VALARM")
	}
	if event.Organizer != "" {
		line("ORGANIZER:mailto:" + event.Organizer)
	}
	line("END:VEVENT")
}

func foldICal(value string) string {
	var out strings.Builder
	width := 75
	for len(value) > width {
		split := width
		for split > 0 && !utf8.RuneStart(value[split]) {
			split--
		}
		out.WriteString(value[:split])
		out.WriteString("\r\n ")
		value = value[split:]
		width = 74
	}
	out.WriteString(value)
	return out.String()
}

// ParseICalendar accepts exactly one series, including its override components.
func ParseICalendar(raw string) (Event, error) {
	events, err := ParseCalendar(raw)
	if err != nil || len(events) != 1 {
		return Event{}, ErrInvalidEvent
	}
	return events[0], nil
}

// ParseCalendar accepts a bounded standard calendar without executing URLs or
// alarms. Floating timestamps use UTC; unknown IANA zones are refused.
func ParseCalendar(raw string) ([]Event, error) {
	if len(raw) > 1<<20 || !utf8.ValidString(raw) {
		return nil, ErrInvalidEvent
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := []string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += line[1:]
		} else {
			lines = append(lines, line)
		}
	}
	components := []Event{}
	var block []string
	inEvent := false
	calendars := 0
	calendarZone := ""
	alarmDepth := 0
	var alarmLines []string
	var alarms []int
	for _, line := range lines {
		switch strings.ToUpper(line) {
		case "BEGIN:VCALENDAR":
			calendars++
		case "BEGIN:VEVENT":
			if inEvent {
				return nil, ErrInvalidEvent
			}
			inEvent = true
			block = nil
			if calendarZone != "" {
				block = append(block, "X-WR-TIMEZONE:"+calendarZone)
			}
			alarms = nil
		case "END:VEVENT":
			if !inEvent {
				return nil, ErrInvalidEvent
			}
			event, err := parseEventLines(block)
			event.ReminderMinutes = alarms
			if err != nil {
				return nil, err
			}
			components = append(components, event)
			if len(components) > 1000 {
				return nil, ErrInvalidEvent
			}
			inEvent = false
		case "BEGIN:VALARM":
			if inEvent {
				alarmDepth++
				if alarmDepth > 1 {
					return nil, ErrInvalidEvent
				}
				alarmLines = nil
			}
		case "END:VALARM":
			if inEvent {
				alarmDepth--
				if minutes, ok := parseDisplayAlarm(alarmLines); ok {
					alarms = append(alarms, minutes)
					if len(alarms) > 5 {
						return nil, ErrInvalidEvent
					}
				}
			}
			if alarmDepth < 0 {
				return nil, ErrInvalidEvent
			}
		default:
			if !inEvent && strings.HasPrefix(strings.ToUpper(line), "X-WR-TIMEZONE:") {
				_, zone, _ := cutICalProperty(line)
				if _, err := time.LoadLocation(zone); err != nil {
					return nil, ErrInvalidEvent
				}
				calendarZone = zone
			}
			if inEvent && alarmDepth == 0 {
				block = append(block, line)
			} else if inEvent && alarmDepth == 1 {
				alarmLines = append(alarmLines, line)
			}
		}
	}
	if calendars != 1 || inEvent || alarmDepth != 0 || len(components) == 0 {
		return nil, ErrInvalidEvent
	}
	result := []Event{}
	indexes := map[string]int{}
	for _, event := range components {
		if event.RecurrenceID != "" {
			continue
		}
		if event.UID != "" {
			if _, ok := indexes[event.UID]; ok {
				return nil, ErrInvalidEvent
			}
			indexes[event.UID] = len(result)
		}
		result = append(result, event)
	}
	for _, event := range components {
		if event.RecurrenceID == "" {
			continue
		}
		index, ok := indexes[event.UID]
		if !ok || event.UID == "" {
			return nil, ErrInvalidEvent
		}
		id, err := time.Parse(time.RFC3339, event.RecurrenceID)
		if err != nil {
			return nil, ErrInvalidEvent
		}
		result[index].Overrides = append(result[index].Overrides, OccurrenceOverride{RecurrenceID: id, Summary: event.Summary, Description: event.Description, Location: event.Location, StartsAt: event.StartsAt, EndsAt: event.EndsAt, AllDay: event.AllDay, Status: event.Status})
	}
	for i := range result {
		normalized, err := NormalizeEvent(result[i])
		if err != nil {
			return nil, err
		}
		result[i] = normalized
	}
	return result, nil
}

func parseEventLines(lines []string) (Event, error) {
	event := Event{}
	defaultZone := "UTC"
	for _, line := range lines {
		if name, value, ok := cutICalProperty(line); ok && strings.EqualFold(name, "X-WR-TIMEZONE") {
			if _, err := time.LoadLocation(value); err != nil {
				return Event{}, ErrInvalidEvent
			}
			defaultZone = value
		}
	}
	properties := map[string]string{}
	for _, line := range lines {
		name, value, ok := cutICalProperty(line)
		if !ok {
			continue
		}
		parts := splitICalParams(name)
		key := strings.ToUpper(parts[0])
		zone := defaultZone
		allDay := false
		for _, param := range parts[1:] {
			k, v, _ := strings.Cut(param, "=")
			if strings.EqualFold(k, "TZID") {
				zone = strings.Trim(v, "\"")
			}
			if strings.EqualFold(param, "VALUE=DATE") {
				allDay = true
			}
		}
		switch key {
		case "DTSTART", "DTEND", "RECURRENCE-ID":
			location, err := time.LoadLocation(zone)
			if err != nil {
				return Event{}, ErrInvalidEvent
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
				return Event{}, ErrInvalidEvent
			}
			switch key {
			case "DTSTART":
				event.StartsAt = stamp.UTC()
				event.Timezone = zone
				event.AllDay = allDay
			case "DTEND":
				event.EndsAt = stamp.UTC()
			default:
				event.RecurrenceID = stamp.UTC().Format(time.RFC3339)
			}
		case "ORGANIZER":
			if !strings.HasPrefix(strings.ToLower(value), "mailto:") {
				return Event{}, ErrInvalidEvent
			}
			event.Organizer = strings.ToLower(value[len("mailto:"):])
		case "SEQUENCE":
			n, err := strconv.ParseInt(value, 10, 32)
			if err != nil || n < 0 {
				return Event{}, ErrInvalidEvent
			}
			event.Sequence = int(n)
		case "UID":
			event.UID = unescapeVCard(value)
		case "SUMMARY":
			event.Summary = unescapeVCard(value)
		case "DESCRIPTION":
			event.Description = unescapeVCard(value)
		case "LOCATION":
			event.Location = unescapeVCard(value)
		case "STATUS":
			event.Status = strings.ToLower(value)
		case "X-WR-TIMEZONE":
			if _, err := time.LoadLocation(value); err != nil {
				return Event{}, ErrInvalidEvent
			}
			event.Timezone = value
		case "RRULE", "EXDATE":
			if key == "EXDATE" && properties[key] != "" {
				properties[key] += "," + value
			} else {
				properties[key] = value
			}
		case "ATTENDEE":
			email, status, ok := parseAttendeeLine(line)
			if ok {
				event.Attendees = append(event.Attendees, email)
				if status != "" {
					if event.AttendeeStatus == nil {
						event.AttendeeStatus = map[string]string{}
					}
					event.AttendeeStatus[email] = status
				}
			}
		}
	}
	event.RRule = properties["RRULE"]
	if exdate := properties["EXDATE"]; exdate != "" {
		event.RRule += ";EXDATE=" + exdate
	}
	return NormalizeEvent(event)
}

// Calendar exports a bounded set of complete series with their exceptions.
func Calendar(events []Event) (string, error) {
	if len(events) > 1000 {
		return "", ErrInvalidEvent
	}
	var b strings.Builder
	b.WriteString(calendarHeader)
	for _, event := range events {
		one, err := ICalendar(event)
		if err != nil {
			return "", err
		}
		one = strings.TrimSuffix(strings.TrimPrefix(one, calendarHeader), "END:VCALENDAR\r\n")
		b.WriteString(one)
	}
	b.WriteString("END:VCALENDAR\r\n")
	return b.String(), nil
}

func splitException(rule string) (string, string) {
	parts := strings.Split(rule, ";")
	base := []string{}
	exceptions := []string{}
	for _, part := range parts {
		if key, value, ok := strings.Cut(part, "="); ok && strings.EqualFold(key, "EXDATE") {
			exceptions = append(exceptions, strings.ToUpper(value))
		} else {
			base = append(base, part)
		}
	}
	sort.Strings(exceptions)
	return strings.Join(base, ";"), strings.Join(exceptions, ",")
}

// parseDisplayAlarm accepts bounded start-relative DISPLAY alarms only. Other
// actions, repetition, external URLs and absolute triggers are never executed.
func parseDisplayAlarm(lines []string) (int, bool) {
	action, trigger, description := "", "", ""
	for _, line := range lines {
		key, value, ok := cutICalProperty(line)
		if !ok {
			return 0, false
		}
		switch strings.ToUpper(key) {
		case "ACTION":
			action = value
		case "TRIGGER", "TRIGGER;RELATED=START":
			trigger = value
		case "DESCRIPTION":
			description = value
		default:
			return 0, false
		}
	}
	if action != "DISPLAY" || description == "" || trigger == "" {
		return 0, false
	}
	switch {
	case strings.HasPrefix(trigger, "-P"):
		trigger = strings.TrimPrefix(trigger, "-P")
	case trigger == "PT0M":
		return 0, true
	default:
		return 0, false
	}
	duration := time.Duration(0)
	if days, rest, ok := strings.Cut(trigger, "D"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 || n > 28 {
			return 0, false
		}
		duration = time.Duration(n) * 24 * time.Hour
		trigger = rest
	}
	if trigger != "" {
		if !strings.HasPrefix(trigger, "T") {
			return 0, false
		}
		clock := strings.ToLower(strings.TrimPrefix(trigger, "T"))
		d, err := time.ParseDuration(clock)
		if err != nil || d < 0 {
			return 0, false
		}
		duration += d
	}
	if duration%time.Minute != 0 || duration < 0 || duration > 28*24*time.Hour {
		return 0, false
	}
	return int(duration / time.Minute), true
}

// cutICalProperty respects quoted parameter values containing a colon. A
// display name cannot change the field's actual value delimiter.
func cutICalProperty(line string) (string, string, bool) {
	quoted, escaped := false, false
	for index, char := range line {
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '"' {
			quoted = !quoted
			continue
		}
		if char == ':' && !quoted {
			return line[:index], line[index+1:], true
		}
	}
	return "", "", false
}

func splitICalParams(value string) []string {
	var out []string
	start := 0
	quoted, escaped := false, false
	for index, char := range value {
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '"' {
			quoted = !quoted
			continue
		}
		if char == ';' && !quoted {
			out = append(out, value[start:index])
			start = index + 1
		}
	}
	return append(out, value[start:])
}
