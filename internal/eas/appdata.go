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

// calendarAppData renders MS-ASCAL compact UTC dates and sibling Exceptions.
func calendarAppData(e collab.Event, occurrence time.Time, rec *recurrenceData, uid string) *Node {
	app := Element(pageAirSync, tokApplicationData)
	start, end := e.StartsAt, e.EndsAt
	if !occurrence.IsZero() {
		start = occurrence
		end = occurrence.Add(e.EndsAt.Sub(e.StartsAt))
		for _, ov := range e.Overrides {
			if ov.RecurrenceID.Equal(occurrence) {
				e.Summary, e.Description, e.Location, e.AllDay, e.Status = ov.Summary, ov.Description, ov.Location, ov.AllDay, ov.Status
				start, end = ov.StartsAt, ov.EndsAt
				break
			}
		}
	}
	app.Children = append(app.Children, Text(pageCalendar, tokCalSubject, e.Summary), Text(pageCalendar, tokCalLocation, e.Location), Text(pageCalendar, tokCalStartTime, start.UTC().Format(calendarTime)), Text(pageCalendar, tokCalEndTime, end.UTC().Format(calendarTime)), Text(pageCalendar, tokCalDtStamp, time.Now().UTC().Format(calendarTime)), Text(pageCalendar, tokCalAllDayEvent, boolInt(e.AllDay)))
	if len(e.ReminderMinutes) > 0 {
		minutes := e.ReminderMinutes[0]
		for _, value := range e.ReminderMinutes {
			minutes = max(minutes, value)
		}
		app.Add(Text(pageCalendar, 0x24, strconv.Itoa(minutes)))
	}
	if e.Organizer != "" {
		app.Add(Text(pageCalendar, 0x19, e.Organizer))
	}
	if zone := calendarZoneWire(e.Timezone, start); zone != "" {
		app.Add(Text(pageCalendar, tokCalTimezone, zone))
	}
	if uid != "" {
		app.Add(Text(pageCalendar, tokCalUID, uid))
	}
	busy := "2"
	if e.Status == "tentative" {
		busy = "1"
	}
	if e.Status == "cancelled" { //nolint:misspell // RFC 5545 value
		busy = "0"
	} //nolint:misspell // RFC 5545 value
	app.Add(Text(pageCalendar, tokCalBusyStatus, busy))
	if e.Description != "" {
		app.Add(Element(pageAirSyncBase, tokBaseBody, Text(pageAirSyncBase, tokBaseType, "1"), Text(pageAirSyncBase, tokBaseData, e.Description)))
	}
	if rec != nil {
		rn := Element(pageCalendar, tokCalRecurrence, Text(pageCalendar, tokCalRecType, strconv.Itoa(rec.typ)), Text(pageCalendar, tokCalRecInterval, strconv.Itoa(rec.interval)))
		loc, err := time.LoadLocation(e.Timezone)
		if err != nil {
			loc = time.UTC
		}
		local := e.StartsAt.In(loc)
		if rec.typ == recTypeWeekly {
			days := rec.days
			if days == 0 {
				days = 1 << int(local.Weekday())
			}
			rn.Add(Text(pageCalendar, tokCalDayOfWeek, strconv.Itoa(days)))
			rn.Add(Text(pageCalendar, tokCalFirstDayOfWeek, strconv.Itoa(rec.weekStart)))
		}
		if rec.typ == recTypeMonthly || rec.typ == recTypeYearly {
			day := rec.day
			if day == 0 {
				day = local.Day()
			}
			rn.Add(Text(pageCalendar, tokCalDayOfMonth, strconv.Itoa(day)))
		}
		if rec.typ == recTypeYearly {
			month := rec.month
			if month == 0 {
				month = int(local.Month())
			}
			rn.Add(Text(pageCalendar, tokCalMonthOfYear, strconv.Itoa(month)))
		}
		if !rec.until.IsZero() {
			rn.Add(Text(pageCalendar, tokCalRecUntil, rec.until.UTC().Format(calendarTime)))
		}
		if rec.count > 0 {
			rn.Add(Text(pageCalendar, tokCalRecCount, strconv.Itoa(rec.count)))
		}
		app.Add(rn)
		exceptions := Element(pageCalendar, tokCalExceptions)
		for _, ex := range rec.exceptions {
			exceptions.Add(Element(pageCalendar, tokCalException, Text(pageCalendar, tokCalExcDeleted, "1"), Text(pageCalendar, tokCalExcStartTime, ex.UTC().Format(calendarTime))))
		}
		for _, ov := range e.Overrides {
			deleted := ov.Status == "cancelled" //nolint:misspell // RFC 5545 value
			ex := Element(pageCalendar, tokCalException, Text(pageCalendar, tokCalExcDeleted, boolInt(deleted)), Text(pageCalendar, tokCalExcStartTime, ov.RecurrenceID.UTC().Format(calendarTime)))
			if !deleted {
				ex.Children = append(ex.Children, Text(pageCalendar, tokCalSubject, ov.Summary), Text(pageCalendar, tokCalLocation, ov.Location), Text(pageCalendar, tokCalStartTime, ov.StartsAt.UTC().Format(calendarTime)), Text(pageCalendar, tokCalEndTime, ov.EndsAt.UTC().Format(calendarTime)), Text(pageCalendar, tokCalAllDayEvent, boolInt(ov.AllDay)), Element(pageAirSyncBase, tokBaseBody, Text(pageAirSyncBase, tokBaseType, "1"), Text(pageAirSyncBase, tokBaseData, ov.Description)))
			}
			exceptions.Add(ex)
		}
		if len(exceptions.Children) > 0 {
			app.Add(exceptions)
		}
	}
	return app
}

type recurrenceData struct {
	typ, interval, count, days, day, month, weekStart int
	until                                             time.Time
	exceptions                                        []time.Time
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
	rec := recurrenceData{interval: 1, exceptions: exceptions, weekStart: 1}
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
		case "BYDAY":
			for _, day := range strings.Split(value, ",") {
				index := -1
				for i, name := range []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"} {
					if name == day {
						index = i
						break
					}
				}
				if index < 0 {
					return recurrenceData{}, false
				}
				rec.days |= 1 << index
			}
		case "BYMONTHDAY":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 31 {
				return recurrenceData{}, false
			}
			rec.day = n
		case "BYMONTH":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 12 {
				return recurrenceData{}, false
			}
			rec.month = n
		case "WKST":
			found := false
			for i, name := range []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"} {
				if value == name {
					rec.weekStart = i
					found = true
					break
				}
			}
			if !found {
				return recurrenceData{}, false
			}
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
	if rec.typ != recTypeWeekly && rec.days != 0 {
		return recurrenceData{}, false
	}
	if rec.typ != recTypeMonthly && rec.typ != recTypeYearly && rec.day != 0 {
		return recurrenceData{}, false
	}
	if rec.typ != recTypeYearly && rec.month != 0 {
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
