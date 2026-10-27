package eas

// Application data encoders for the three implemented AirSync classes.
// Token values come from the MS-ASWBXML code page tables mirrored in
// wbxml.go (Contacts=1, Email=2, Calendar=4, AirSyncBase=17). Elements the
// stored data cannot populate are simply omitted; nothing is invented.

import (
	"strconv"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/collab"
)

// easTime is the wire format for class date elements (MS-ASDTYPE dateTime).
const easTime = "2006-01-02T15:04:05.000Z"

// emailAppData renders one message as MS-ASEMAIL application data. To/Cc and
// the body preview are optional: callers pass an empty preview when the
// per-request fetch budget is spent, and the body then carries Truncated so
// no client mistakes an absent body for an empty one.
func emailAppData(m MailMessage, preview string) *Node {
	app := Element(pageAirSync, tokApplicationData)
	if m.Subject != "" {
		app.Add(Text(pageEmail, tokEmailSubject, m.Subject))
	}
	if len(m.To) > 0 {
		app.Add(Text(pageEmail, tokEmailTo, strings.Join(m.To, ", ")))
	}
	if len(m.Cc) > 0 {
		app.Add(Text(pageEmail, tokEmailCc, strings.Join(m.Cc, ", ")))
	}
	if len(m.From) > 0 {
		app.Add(Text(pageEmail, tokEmailFrom, m.From[0]))
	}
	if !m.Date.IsZero() {
		app.Add(Text(pageEmail, tokEmailDateReceived, m.Date.UTC().Format(easTime)))
	}
	app.Add(Text(pageEmail, tokEmailRead, boolInt(m.Read)))
	if m.Flagged {
		app.Add(Element(pageEmail, tokEmailFlag,
			Text(pageEmail, tokEmailFlagStatus, "2"))) // 2 = active flag
	}
	body := Element(pageAirSyncBase, tokBaseBody)
	body.Add(Text(pageAirSyncBase, tokBaseType, "1")) // 1 = plain text
	body.Add(Text(pageAirSyncBase, tokBaseTruncated, "1"))
	if preview != "" {
		body.Add(Text(pageAirSyncBase, tokBaseData, preview))
		body.Add(Text(pageAirSyncBase, tokBaseEstimatedDataSize,
			strconv.Itoa(len(preview))))
	}
	app.Add(body)
	return app
}

// contactAppData renders one stored contact as MS-ASCNTC application data.
// Email slots 1-3 and the home/business/mobile phone slots are the only
// mapped fields; further vCard fields have no collab storage yet.
func contactAppData(c collab.Contact) *Node {
	app := Element(pageAirSync, tokApplicationData)
	if c.GivenName != "" {
		app.Add(Text(pageContacts, tokContactFirstName, c.GivenName))
	}
	if c.FamilyName != "" {
		app.Add(Text(pageContacts, tokContactLastName, c.FamilyName))
	}
	fileAs := c.DisplayName
	if fileAs == "" {
		fileAs = strings.TrimSpace(c.GivenName + " " + c.FamilyName)
	}
	if fileAs != "" {
		app.Add(Text(pageContacts, tokContactFileAs, fileAs))
	}
	emailTokens := []byte{tokContactEmail1Address, tokContactEmail2Address, tokContactEmail3Address}
	for i, e := range c.Emails {
		if i >= len(emailTokens) {
			break
		}
		app.Add(Text(pageContacts, emailTokens[i], e.Address))
	}
	phoneTokens := map[string]byte{
		"home":   tokContactHomePhoneNumber,
		"work":   tokContactBusinessPhoneNumber,
		"mobile": tokContactMobilePhoneNumber,
	}
	fallback := byte(0)
	for _, p := range c.Phones {
		tok, ok := phoneTokens[strings.ToLower(p.Label)]
		if !ok {
			if fallback == 0 {
				fallback = tokContactBusinessPhoneNumber
			}
			tok = fallback
		}
		app.Add(Text(pageContacts, tok, p.Number))
	}
	if c.Note != "" {
		body := Element(pageAirSyncBase, tokBaseBody)
		body.Add(Text(pageAirSyncBase, tokBaseType, "1"))
		body.Add(Text(pageAirSyncBase, tokBaseData, c.Note))
		app.Add(body)
	}
	return app
}

// calendarAppData renders one stored event as MS-ASCAL application data.
// recurrence may be nil for single events. When a stored series cannot be
// represented in the implemented Recurrence subset, callers expand the series
// into individual occurrences and pass recTypeOccurrence instead.
func calendarAppData(e collab.Event, occurrence time.Time, rec *recurrenceData, uid string) *Node {
	app := Element(pageAirSync, tokApplicationData)
	if e.Summary != "" {
		app.Add(Text(pageCalendar, tokCalSubject, e.Summary))
	}
	if e.Location != "" {
		app.Add(Text(pageCalendar, tokCalLocation, e.Location))
	}
	start := e.StartsAt
	end := e.EndsAt
	if !occurrence.IsZero() {
		// Expanded occurrence: shift the series bounds onto this instance.
		start = occurrence
		end = occurrence.Add(e.EndsAt.Sub(e.StartsAt))
	}
	app.Add(Text(pageCalendar, tokCalStartTime, start.UTC().Format(easTime)))
	app.Add(Text(pageCalendar, tokCalEndTime, end.UTC().Format(easTime)))
	app.Add(Text(pageCalendar, tokCalDtStamp, time.Now().UTC().Format(easTime)))
	if uid != "" {
		app.Add(Text(pageCalendar, tokCalUID, uid))
	}
	app.Add(Text(pageCalendar, tokCalAllDayEvent, boolInt(e.AllDay)))
	// BusyStatus: 1 = tentative, 2 = busy; canceled events stay busy.
	busy := "2"
	if e.Status == "tentative" {
		busy = "1"
	}
	app.Add(Text(pageCalendar, tokCalBusyStatus, busy))
	if rec != nil {
		recNode := Element(pageCalendar, tokCalRecurrence)
		recNode.Add(Text(pageCalendar, tokCalRecType, strconv.Itoa(rec.typ)))
		recNode.Add(Text(pageCalendar, tokCalRecInterval, strconv.Itoa(rec.interval)))
		if !rec.until.IsZero() {
			recNode.Add(Text(pageCalendar, tokCalRecUntil, rec.until.UTC().Format(easTime)))
		}
		if rec.count > 0 {
			recNode.Add(Text(pageCalendar, tokCalRecCount, strconv.Itoa(rec.count)))
		}
		if len(rec.exceptions) > 0 {
			exs := Element(pageCalendar, tokCalExceptions)
			for _, ex := range rec.exceptions {
				exs.Add(Element(pageCalendar, tokCalException,
					Text(pageCalendar, tokCalExcDeleted, "1"),
					Text(pageCalendar, tokCalExcStartTime, ex.UTC().Format(easTime))))
			}
			recNode.Add(exs)
		}
		app.Add(recNode)
	}
	return app
}

// recurrenceData is the representable subset of a stored RRULE.
type recurrenceData struct {
	typ        int         // MS-ASCAL Recurrence Type
	interval   int         // every N days/weeks/months/years
	count      int         // COUNT, 0 = none
	until      time.Time   // UNTIL, zero = none
	exceptions []time.Time // EXDATE occurrences, only the first is stored
}

// recurrence types per MS-ASCAL.
const (
	recTypeDaily   = 0
	recTypeWeekly  = 1
	recTypeMonthly = 2
	recTypeYearly  = 5
)

// parseRecurrence converts the stored RRULE string (with its single EXDATE
// suffix) into the representable subset. ok is false when the rule uses
// parts this implementation cannot map, so callers can fall back to
// expanding occurrences inside the sync window.
func parseRecurrence(rrule string) (recurrenceData, bool) {
	if strings.TrimSpace(rrule) == "" {
		return recurrenceData{}, false
	}
	base := rrule
	var exceptions []time.Time
	if i := strings.Index(strings.ToUpper(rrule), ";EXDATE="); i >= 0 {
		base = rrule[:i]
		value := rrule[i+len(";EXDATE="):]
		for _, part := range strings.Split(value, ",") {
			if t, err := time.Parse("20060102T150405Z", strings.TrimSpace(part)); err == nil {
				exceptions = append(exceptions, t)
			}
		}
	}
	rec := recurrenceData{interval: 1, exceptions: exceptions}
	for _, part := range strings.Split(strings.ToUpper(base), ";") {
		key, value, _ := strings.Cut(part, "=")
		switch key {
		case "FREQ":
			switch value {
			case "DAILY":
				rec.typ = recTypeDaily
			case "WEEKLY":
				rec.typ = recTypeWeekly
			case "MONTHLY":
				rec.typ = recTypeMonthly
			case "YEARLY":
				rec.typ = recTypeYearly
			default:
				return recurrenceData{}, false
			}
		case "INTERVAL":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return recurrenceData{}, false
			}
			rec.interval = n
		case "COUNT":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return recurrenceData{}, false
			}
			rec.count = n
		case "UNTIL":
			t, err := time.Parse("20060102T150405Z", value)
			if err != nil {
				return recurrenceData{}, false
			}
			rec.until = t
		default:
			// BYDAY, BYMONTHDAY and friends are not mapped.
			return recurrenceData{}, false
		}
	}
	if rec.typ == recTypeWeekly && rec.interval != 1 {
		return recurrenceData{}, false
	}
	return rec, true
}

func boolInt(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
