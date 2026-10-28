package eas

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/collab"
)

func calendarChild(n *Node, page, token byte) *Node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Page == page && c.Token == token {
			return c
		}
	}
	return nil
}
func calendarText(n *Node, token byte) string {
	if c := calendarChild(n, pageCalendar, token); c != nil {
		return c.Text
	}
	return ""
}
func parseCalendarTime(value string) (time.Time, error) { return time.Parse(calendarTime, value) }

// parseCalendarData applies partial Change fields to the current server snapshot.
// Unsupported recurrence patterns fail explicitly; no rule is silently dropped.
func parseCalendarData(app *Node, current collab.Event) (collab.Event, error) {
	if app == nil {
		return current, collab.ErrInvalidEvent
	}
	e := current
	for _, field := range []struct {
		token byte
		dest  *string
	}{{tokCalSubject, &e.Summary}, {tokCalLocation, &e.Location}, {tokCalUID, &e.UID}} {
		if n := calendarChild(app, pageCalendar, field.token); n != nil {
			*field.dest = n.Text
		}
	}
	if reminder := calendarChild(app, pageCalendar, 0x24); reminder != nil {
		if reminder.Text == "" {
			e.ReminderMinutes = []int{}
		} else {
			minutes, err := strconv.Atoi(reminder.Text)
			if err != nil || minutes < 0 || minutes > 40320 {
				return e, collab.ErrInvalidEvent
			}
			e.ReminderMinutes = []int{minutes}
		}
	}
	var err error
	if n := calendarChild(app, pageCalendar, tokCalStartTime); n != nil {
		e.StartsAt, err = parseCalendarTime(n.Text)
		if err != nil {
			return e, err
		}
	}
	if n := calendarChild(app, pageCalendar, tokCalEndTime); n != nil {
		e.EndsAt, err = parseCalendarTime(n.Text)
		if err != nil {
			return e, err
		}
	}
	if n := calendarChild(app, pageCalendar, tokCalAllDayEvent); n != nil {
		if n.Text != "0" && n.Text != "1" {
			return e, collab.ErrInvalidEvent
		}
		e.AllDay = n.Text == "1"
	}
	if n := calendarChild(app, pageCalendar, tokCalTimezone); n != nil {
		if e.StartsAt.IsZero() {
			return e, collab.ErrInvalidEvent
		}
		e.Timezone, err = parseCalendarZone(n.Text, e.StartsAt)
		if err != nil {
			return e, err
		}
	}
	if n := calendarChild(app, pageCalendar, tokCalBusyStatus); n != nil {
		switch n.Text {
		case "0", "2", "3", "4":
			e.Status = "confirmed"
		case "1":
			e.Status = "tentative"
		default:
			return e, collab.ErrInvalidEvent
		}
	}
	if body := calendarChild(app, pageAirSyncBase, tokBaseBody); body != nil {
		if typ := body.ChildText(tokBaseType); typ != "1" {
			return e, collab.ErrInvalidEvent
		}
		e.Description = body.ChildText(tokBaseData)
	}
	if rec := calendarChild(app, pageCalendar, tokCalRecurrence); rec != nil {
		e.RRule, err = calendarRule(rec)
		if err != nil {
			return e, err
		}
	}
	if exceptions := calendarChild(app, pageCalendar, tokCalExceptions); exceptions != nil {
		if e.RRule == "" {
			return e, collab.ErrInvalidEvent
		}
		e.Overrides = make([]collab.OccurrenceOverride, 0, len(exceptions.Children))
		for _, ex := range exceptions.Children {
			if ex.Page != pageCalendar || ex.Token != tokCalException {
				return e, collab.ErrInvalidEvent
			}
			at, err := parseCalendarTime(calendarText(ex, tokCalExcStartTime))
			if err != nil {
				return e, err
			}
			ov := collab.OccurrenceOverride{RecurrenceID: at, Summary: e.Summary, Description: e.Description, Location: e.Location, StartsAt: at, EndsAt: at.Add(e.EndsAt.Sub(e.StartsAt)), AllDay: e.AllDay, Status: e.Status}
			switch calendarText(ex, tokCalExcDeleted) {
			case "1":
				ov.Status = "cancelled" //nolint:misspell // RFC 5545 value
			case "", "0":
			default:
				return e, collab.ErrInvalidEvent
			} //nolint:misspell // RFC 5545 value
			for _, f := range []struct {
				token byte
				dest  *string
			}{{tokCalSubject, &ov.Summary}, {tokCalLocation, &ov.Location}} {
				if n := calendarChild(ex, pageCalendar, f.token); n != nil {
					*f.dest = n.Text
				}
			}
			if n := calendarChild(ex, pageCalendar, tokCalStartTime); n != nil {
				ov.StartsAt, err = parseCalendarTime(n.Text)
				if err != nil {
					return e, err
				}
			}
			if n := calendarChild(ex, pageCalendar, tokCalEndTime); n != nil {
				ov.EndsAt, err = parseCalendarTime(n.Text)
				if err != nil {
					return e, err
				}
			}
			if n := calendarChild(ex, pageCalendar, tokCalAllDayEvent); n != nil {
				if n.Text != "0" && n.Text != "1" {
					return e, collab.ErrInvalidEvent
				}
				ov.AllDay = n.Text == "1"
			}
			if body := calendarChild(ex, pageAirSyncBase, tokBaseBody); body != nil {
				if body.ChildText(tokBaseType) != "1" {
					return e, collab.ErrInvalidEvent
				}
				ov.Description = body.ChildText(tokBaseData)
			}
			e.Overrides = append(e.Overrides, ov)
		}
	}
	return collab.NormalizeEvent(e)
}
func calendarRule(n *Node) (string, error) {
	if len(n.Children) == 0 {
		return "", nil
	}
	typ, err := strconv.Atoi(calendarText(n, tokCalRecType))
	if err != nil {
		return "", err
	}
	freq := ""
	switch typ {
	case 0:
		freq = "DAILY"
	case 1:
		freq = "WEEKLY"
	case 2:
		freq = "MONTHLY"
	case 5:
		freq = "YEARLY"
	default:
		return "", errors.New("unsupported EAS recurrence pattern")
	}
	parts := []string{"FREQ=" + freq}
	for _, f := range []struct {
		tok      byte
		key      string
		min, max int
	}{{tokCalRecInterval, "INTERVAL", 1, 366}, {tokCalRecCount, "COUNT", 1, collab.MaxInstances}} {
		if v := calendarText(n, f.tok); v != "" {
			x, err := strconv.Atoi(v)
			if err != nil || x < f.min || x > f.max {
				return "", collab.ErrInvalidEvent
			}
			parts = append(parts, f.key+"="+v)
		}
	}
	if until := calendarText(n, tokCalRecUntil); until != "" {
		if _, err := parseCalendarTime(until); err != nil {
			return "", err
		}
		parts = append(parts, "UNTIL="+until)
	}
	if calendarText(n, tokCalRecCount) != "" && calendarText(n, tokCalRecUntil) != "" {
		return "", collab.ErrInvalidEvent
	}
	if typ == 1 {
		bits, err := strconv.Atoi(calendarText(n, tokCalDayOfWeek))
		if err != nil || bits < 1 || bits > 127 {
			return "", collab.ErrInvalidEvent
		}
		days := []string{}
		for i, day := range []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"} {
			if bits&(1<<i) != 0 {
				days = append(days, day)
			}
		}
		parts = append(parts, "BYDAY="+strings.Join(days, ","))
		weekStart := 1
		if value := calendarText(n, tokCalFirstDayOfWeek); value != "" {
			var err error
			weekStart, err = strconv.Atoi(value)
			if err != nil || weekStart < 0 || weekStart > 6 {
				return "", collab.ErrInvalidEvent
			}
		}
		parts = append(parts, "WKST="+[]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}[weekStart])
	}
	if typ == 2 || typ == 5 {
		day, err := strconv.Atoi(calendarText(n, tokCalDayOfMonth))
		if err != nil || day < 1 || day > 31 {
			return "", collab.ErrInvalidEvent
		}
		parts = append(parts, "BYMONTHDAY="+strconv.Itoa(day))
	}
	if typ == 5 {
		month, err := strconv.Atoi(calendarText(n, tokCalMonthOfYear))
		if err != nil || month < 1 || month > 12 {
			return "", collab.ErrInvalidEvent
		}
		parts = append(parts, "BYMONTH="+strconv.Itoa(month))
	}
	// Alternate calendars and nth-week patterns are not stored by our domain.
	for _, c := range n.Children {
		switch c.Token {
		case tokCalRecType, tokCalRecInterval, tokCalRecCount, tokCalRecUntil, tokCalDayOfWeek, tokCalDayOfMonth, tokCalMonthOfYear, tokCalFirstDayOfWeek:
		default:
			return "", collab.ErrInvalidEvent
		}
	}
	return strings.Join(parts, ";"), nil
}
