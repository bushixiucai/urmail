// Package caldav renders and parses iCalendar event bodies. Bodies are always
// rendered from PostgreSQL columns at request time; nothing here stores a
// second copy. RRULE, EXDATE and attendees round-trip through the shared
// domain package, which stays the single implementation.
package caldav

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// crlf is the iCalendar line separator built from byte codes so the
// source stays plain.
var crlf = string([]byte{13, 10})

// xmlHeader opens every XML body.
const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

// quote is the DQUOTE character as a string.
var quote = string([]byte{34})

// backslash is the escape character of iCalendar text values.
var backslash = string([]byte{92})

// icalUnescape reverses icalEscape on one value.
func icalUnescape(s string) string {
	var b strings.Builder
	escaped := false
	for _, r := range s {
		if escaped {
			switch r {
			case 110:
				b.WriteRune(10)
			case 92, 59, 44:
				b.WriteRune(r)
			default:
				b.WriteRune(92)
				b.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == 92 {
			escaped = true
			continue
		}
		b.WriteRune(r)
	}
	if escaped {
		b.WriteString(backslash)
	}
	return b.String()
}

// eventExtras carries the fields the shared minimal parser ignores.
type eventExtras struct {
	UID         string
	Description string
	Location    string
	Status      string
}

// unfold rejoins RFC 5545 folded lines so both parsers see whole lines.
func unfold(raw string) string {
	raw = strings.ReplaceAll(raw, crlf+" ", "")
	raw = strings.ReplaceAll(raw, crlf+string([]byte{9}), "")
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if len(line) > 0 && (line[0] == 32 || line[0] == 9) && len(out) > 0 {
			out[len(out)-1] += line[1:]
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, crlf)
}

// parseEventWithExtras reads one event plus the adapter-level fields.
func parseEventWithExtras(raw string) (collab.Event, eventExtras, error) {
	flat := unfold(raw)
	var extras eventExtras
	for _, line := range strings.Split(flat, crlf) {
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = icalUnescape(value)
		switch strings.ToUpper(strings.TrimSpace(name)) {
		case "UID":
			if extras.UID == "" {
				extras.UID = strings.TrimSpace(value)
			}
		case "DESCRIPTION":
			if extras.Description == "" {
				extras.Description = value
			}
		case "LOCATION":
			if extras.Location == "" {
				extras.Location = value
			}
		case "STATUS":
			if extras.Status == "" {
				extras.Status = strings.ToLower(strings.TrimSpace(value))
			}
		}
	}
	event, err := collab.ParseICalendar(flat)
	if err != nil {
		return collab.Event{}, extras, err
	}
	event.Description = extras.Description
	event.Location = extras.Location
	event.Status = extras.Status
	event, err = collab.NormalizeEvent(event)
	return event, extras, err
}

// eventBody renders one stored event as an iCalendar resource.
func eventBody(row store.MailboxEvents) (string, error) {
	ev, err := rowEvent(row)
	if err != nil {
		return "", err
	}
	base, err := collab.ICalendar(ev)
	if err != nil {
		return "", err
	}
	return base, nil
}

// rowEvent collects the columns the domain renderer needs.
func rowEvent(row store.MailboxEvents) (collab.Event, error) {
	var attendees []string
	if len(row.Attendees) > 0 {
		if err := json.Unmarshal(row.Attendees, &attendees); err != nil || attendees == nil {
			attendees = []string{}
		}
	}
	overrides, err := collab.DecodeOccurrenceOverrides(row.RecurrenceOverrides)
	if err != nil {
		return collab.Event{}, err
	}
	ev := collab.Event{
		UID: row.Uid, Overrides: overrides,
		Summary: row.Summary, Description: row.Description, Location: row.Location,
		StartsAt: row.StartsAt.Time, EndsAt: row.EndsAt.Time, AllDay: row.AllDay,
		Timezone: row.Timezone, RRule: row.Rrule, Status: row.Status, Attendees: attendees,
	}
	// Normalize validates the stored row the same way a fresh write is
	// validated; stored rows always pass.
	return collab.NormalizeEvent(ev)
}

// trimETag strips optional quoting from an ETag header value.
// resourcePreconditions applies strong If-Match and weak If-None-Match
// comparison. If-Match is evaluated first when both headers are present.
func resourcePreconditions(r *http.Request, exists bool, current string) bool {
	match := func(raw string, weak bool) bool {
		for _, candidate := range strings.Split(raw, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" {
				return exists
			}
			if strings.HasPrefix(candidate, "W/") {
				if !weak {
					continue
				}
				candidate = strings.TrimPrefix(candidate, "W/")
			}
			if exists && candidate == quote+current+quote {
				return true
			}
		}
		return false
	}
	if raw := strings.TrimSpace(r.Header.Get("If-Match")); raw != "" && !match(raw, false) {
		return false
	}
	if raw := strings.TrimSpace(r.Header.Get("If-None-Match")); raw != "" && match(raw, true) {
		return false
	}
	return true
}

func etagValue(version int64) string {
	return strconv.FormatInt(version, 10)
}

func etag(version int64) string {
	return quote + strconv.FormatInt(version, 10) + quote
}

func resourceHref(uid string) string {
	return "/caldav/calendar/" + uid + ".ics"
}

// requestProps extracts requested property local names; empty means all.
// The body reader must not be consumed twice, so only PROPFIND bodies are
// decoded here and callers with a body pass it explicitly.
func requestProps(body io.Reader) map[string]bool {
	props := map[string]bool{}
	if body == nil {
		return props
	}
	dec := xml.NewDecoder(io.LimitReader(body, 1<<20))
	inProp := false
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch elem := tok.(type) {
		case xml.StartElement:
			if inProp {
				if depth == 0 {
					props[elem.Name.Local] = true
				}
				depth++
				continue
			}
			if elem.Name.Local == "prop" && elem.Name.Space == "DAV:" {
				inProp = true
				depth = 0
			}
		case xml.EndElement:
			if inProp {
				if depth == 0 {
					inProp = false
					continue
				}
				depth--
			}
		}
	}
	return props
}

// requestDepth clamps the Depth header to 0 or 1.
func requestDepth(r *http.Request) int {
	switch strings.TrimSpace(r.Header.Get("Depth")) {
	case "1", "infinity":
		return 1
	default:
		return 0
	}
}

// stampLayout is the iCalendar UTC timestamp layout.
const stampLayout = "20060102T150405Z"
