package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// CalDAVReport returns the authenticated mailbox calendar as one iCalendar.
// It is a read-only subset, not a complete CalDAV server.
func (h MailboxCalendarHandler) CalDAVReport(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	end := time.Now().UTC().AddDate(1, 0, 0)
	start := end.AddDate(-2, 0, 0)
	rows, err := h.Service.Q.PlatformListEvents(c.Request.Context(), store.PlatformListEventsParams{TenantID: p.TenantID, MailboxID: p.MailboxID, RangeStart: timestamptz(start), RangeEnd: timestamptz(end), PageLimit: 1000})
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	events := make([]collab.Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, collab.Event{Summary: row.Summary, Description: row.Description, Location: row.Location, StartsAt: row.StartsAt.Time, EndsAt: row.EndsAt.Time, Timezone: row.Timezone, RRule: row.Rrule, Status: row.Status})
	}
	body, err := collab.Calendar(events)
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	c.Data(http.StatusOK, "text/calendar; charset=utf-8", []byte(body))
}

// CalDAVPropfind answers PROPFIND discovery with a minimal collection
// listing so CalDAV clients can enumerate the calendar home.
func (h MailboxCalendarHandler) CalDAVPropfind(c *gin.Context) {
	if _, ok := h.principal(c); !ok {
		return
	}
	body := `<?xml version="1.0" encoding="UTF-8"?><multistatus xmlns="DAV:"><response><propstat><prop><resourcetype><collection/></resourcetype></prop><status>HTTP/1.1 200 OK</status></propstat></response></multistatus>`
	c.Data(http.StatusMultiStatus, "application/xml; charset=utf-8", []byte(body))
}

// CalDAVWrite rejects write methods; the calendar is read-only over CalDAV.
func (h MailboxCalendarHandler) CalDAVWrite(c *gin.Context) {
	if _, ok := h.principal(c); !ok {
		return
	}
	authError(c, http.StatusNotImplemented, "CALDAV_WRITE_NOT_IMPLEMENTED")
}
