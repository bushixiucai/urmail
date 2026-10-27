package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

const defaultCalendarName = "Calendar"

// MailboxCalendarHandler serves events for the authenticated mailbox only.
// Attendees and their PARTSTAT answers are stored on the event. Real mail
// only leaves when a submission path and organizer confirmation are all
// present; otherwise the invitations endpoint refuses.
type MailboxCalendarHandler struct {
	Service appcollab.Service
	// InviteSender delivers one invitation; nil keeps invitations refused.
	InviteSender collab.InviteSender
	// Submitter and Vault send real invitation mail on confirmed sends when
	// InviteSender is not pre-wired.
	Submitter webmail.Submitter
	Vault     webmail.CredentialVault
}

// invitationSender resolves the confirmed-send InviteSender for this request:
// the organizer's vaulted submission password is bound to the authenticated
// session token hash, so it can only be resolved here, not at construction.
// Pre-wired InviteSender (tests, custom routing) always wins.
func (h MailboxCalendarHandler) invitationSender(c *gin.Context) (collab.InviteSender, bool) {
	if h.InviteSender != nil {
		return h.InviteSender, true
	}
	if h.Submitter == nil || h.Vault == nil {
		return nil, false
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		return nil, false
	}
	password, ok := h.Vault.Get(session.TokenHash)
	if !ok {
		return nil, false
	}
	return SendInvitationsAsWithPassword(h.Submitter, func(context.Context, string) (string, error) {
		return password, nil
	}), true
}

type eventRequest struct {
	Version        int64             `json:"version"`
	UID            string            `json:"uid"`
	Summary        string            `json:"summary"`
	Description    string            `json:"description"`
	Location       string            `json:"location"`
	StartsAt       string            `json:"starts_at"`
	EndsAt         string            `json:"ends_at"`
	AllDay         bool              `json:"all_day"`
	Timezone       string            `json:"timezone"`
	RRule          string            `json:"rrule"`
	Status         string            `json:"status"`
	RecurrenceID   string            `json:"recurrence_id"`
	Attendees      []string          `json:"attendees"`
	AttendeeStatus map[string]string `json:"attendee_status"`
}

func (h MailboxCalendarHandler) principal(c *gin.Context) (appcollab.Principal, bool) {
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 || session.MailboxID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return appcollab.Principal{}, false
	}
	return appcollab.Principal{TenantID: session.TenantID, MailboxID: session.MailboxID}, true
}

// ListEvents returns events overlapping the requested window.
// Missing bounds default to the current month in UTC.
func (h MailboxCalendarHandler) ListEvents(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	start, end, ok := eventWindow(c)
	if !ok {
		return
	}
	rows, err := h.Service.Q.PlatformListEvents(c.Request.Context(), store.PlatformListEventsParams{
		TenantID: p.TenantID, MailboxID: p.MailboxID,
		RangeStart: timestamptz(start), RangeEnd: timestamptz(end), PageLimit: 200,
	})
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	type listedEvent struct {
		store.MailboxEvents
		Occurrences []time.Time            `json:"occurrences"`
		Attendees   []collab.AttendeeState `json:"attendees"`
	}
	out := make([]listedEvent, 0, len(rows))
	for _, row := range rows {
		emails, status := appcollab.DecodeAttendees(row.Attendees)
		event, normErr := collab.NormalizeEvent(collab.Event{Summary: row.Summary, StartsAt: row.StartsAt.Time, EndsAt: row.EndsAt.Time, Timezone: row.Timezone, RRule: row.Rrule, Status: row.Status, Attendees: emails, AttendeeStatus: status})
		if normErr != nil {
			authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
			return
		}
		times, expandErr := collab.ExpandInstances(event, start, end)
		if expandErr != nil {
			authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
			return
		}
		out = append(out, listedEvent{MailboxEvents: row, Occurrences: times, Attendees: collab.AttendeeStates(event)})
	}
	c.JSON(http.StatusOK, gin.H{"events": out})
}

// CreateEvent stores one normalized event on the default calendar.
func (h MailboxCalendarHandler) CreateEvent(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	var req eventRequest
	if !bindMailboxMessage(c, &req) {
		return
	}
	event, ok := eventFrom(c, req)
	if !ok {
		return
	}
	cal, err := h.Service.EnsureCalendar(c.Request.Context(), p, defaultCalendarName, event.Timezone)
	if errors.Is(err, collab.ErrInvalidEvent) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	row, err := h.Service.CreateEvent(c.Request.Context(), p, cal.ID, req.UID, event)
	if errors.Is(err, collab.ErrInvalidEvent) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if errors.Is(err, appcollab.ErrConflict) {
		authError(c, http.StatusConflict, "EVENT_CONFLICT")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusCreated, row)
}

// UpdateEvent requires the version the user last read.
func (h MailboxCalendarHandler) UpdateEvent(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	var req eventRequest
	if !bindMailboxMessage(c, &req) || req.Version <= 0 {
		if !c.IsAborted() {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		}
		return
	}
	event, ok := eventFrom(c, req)
	if !ok {
		return
	}
	row, err := h.Service.UpdateEvent(c.Request.Context(), p, id, req.Version, event)
	if errors.Is(err, collab.ErrInvalidEvent) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if errors.Is(err, appcollab.ErrConflict) || errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusConflict, "EVENT_CONFLICT")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, row)
}

// DeleteEvent tombstones one event at the expected version.
func (h MailboxCalendarHandler) DeleteEvent(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	version, verr := strconv.ParseInt(c.Query("version"), 10, 64)
	if err != nil || verr != nil || id <= 0 || version <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	session, _ := middleware.GetMailboxSession(c)
	if c.Query("notify") == "attendees" {
		if !h.cancelWithNotice(c, session, p, id, version) {
			return
		}
	}
	if _, err = h.Service.Q.PlatformDeleteEvent(c.Request.Context(), store.PlatformDeleteEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id, Version: version}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			authError(c, http.StatusConflict, "EVENT_CONFLICT")
			return
		}
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// attendeesOf decodes the stored attendee JSON array; corrupt payloads become
// an empty list so an export never fails on legacy rows.
func attendeesOf(raw []byte) []string {
	var attendees []string
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &attendees); err != nil || attendees == nil {
			return []string{}
		}
	}
	return attendees
}

// ExportCalendarICS streams every event in the mailbox default calendar as a
// single iCalendar (RFC 5545) document. Optional from/to bound the stored
// window; the default covers a wide historical/future range so a full export
// is the default behavior.
// GET /mailbox/calendars/export.ics
func (h MailboxCalendarHandler) ExportCalendarICS(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	start := time.Now().AddDate(-10, 0, 0)
	end := time.Now().AddDate(10, 0, 0)
	if raw := c.Query("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
			return
		}
		start = parsed
	}
	if raw := c.Query("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
			return
		}
		end = parsed
	}
	rows, err := h.Service.Q.PlatformListEvents(c.Request.Context(), store.PlatformListEventsParams{
		TenantID: p.TenantID, MailboxID: p.MailboxID,
		RangeStart: timestamptz(start), RangeEnd: timestamptz(end), PageLimit: 1000,
	})
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	events := make([]collab.Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, collab.Event{
			Summary: row.Summary, Description: row.Description, Location: row.Location,
			StartsAt: row.StartsAt.Time, EndsAt: row.EndsAt.Time,
			Timezone: row.Timezone, RRule: row.Rrule, Status: row.Status, Attendees: attendeesOf(row.Attendees),
		})
	}
	body, err := collab.Calendar(events)
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "calendar.ics"}))
	c.Data(http.StatusOK, "text/calendar; charset=utf-8", []byte(body))
}

// ExportEvent returns a single stored event as an attached text/calendar body.
func (h MailboxCalendarHandler) ExportEvent(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	row, err := h.Service.Q.PlatformGetEvent(c.Request.Context(), store.PlatformGetEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusNotFound, "EVENT_NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	var attendees []string
	if len(row.Attendees) > 0 {
		if jsonErr := json.Unmarshal(row.Attendees, &attendees); jsonErr != nil || attendees == nil {
			attendees = []string{}
		}
	}
	body, err := collab.ICalendar(collab.Event{Summary: row.Summary, Description: row.Description, Location: row.Location, StartsAt: row.StartsAt.Time, EndsAt: row.EndsAt.Time, Timezone: row.Timezone, RRule: row.Rrule, Status: row.Status, Attendees: attendees})
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	c.Data(http.StatusOK, "text/calendar; charset=utf-8", []byte(body))
}

// ImportEvent parses one uploaded iCalendar body and stores the event it
// describes for the authenticated mailbox.
func (h MailboxCalendarHandler) ImportEvent(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	raw, err := c.GetRawData()
	if err != nil || len(raw) == 0 || len(raw) > 200000 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	event, err := collab.ParseICalendar(string(raw))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	cal, err := h.Service.EnsureCalendar(c.Request.Context(), p, defaultCalendarName, event.Timezone)
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	row, err := h.Service.CreateEvent(c.Request.Context(), p, cal.ID, "", event)
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusCreated, row)
}

func eventFrom(c *gin.Context, req eventRequest) (collab.Event, bool) {
	start, err := time.Parse(time.RFC3339, req.StartsAt)
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return collab.Event{}, false
	}
	end, err := time.Parse(time.RFC3339, req.EndsAt)
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return collab.Event{}, false
	}
	event, err := collab.NormalizeEvent(collab.Event{
		Summary: req.Summary, Description: req.Description, Location: req.Location,
		StartsAt: start, EndsAt: end, AllDay: req.AllDay, Timezone: req.Timezone,
		RRule: req.RRule, Status: req.Status, RecurrenceID: req.RecurrenceID,
		Attendees: req.Attendees, AttendeeStatus: req.AttendeeStatus,
	})
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return collab.Event{}, false
	}
	return event, true
}

func eventWindow(c *gin.Context) (time.Time, time.Time, bool) {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	if raw := c.Query("start"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
			return time.Time{}, time.Time{}, false
		}
		start = parsed.UTC()
	}
	if raw := c.Query("end"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
			return time.Time{}, time.Time{}, false
		}
		end = parsed.UTC()
	}
	if !end.After(start) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

// ImportEventsCSV parses an uploaded CSV of events and stores the rows it
// describes for the authenticated mailbox.
func (h MailboxCalendarHandler) ImportEventsCSV(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	raw, err := c.GetRawData()
	if err != nil || len(raw) == 0 || len(raw) > 1000000 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	events, err := collab.ParseEventsCSV(string(raw))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	cal, err := h.Service.EnsureCalendar(c.Request.Context(), p, defaultCalendarName, "UTC")
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	for _, event := range events {
		if _, err = h.Service.CreateEvent(c.Request.Context(), p, cal.ID, "", event); err != nil {
			authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
			return
		}
	}
	c.JSON(http.StatusCreated, gin.H{"imported": len(events)})
}

type inviteRequest struct {
	// Confirm is the explicit out-of-band confirmation: no mail leaves
	// without it, regardless of the rest of the payload.
	Confirm bool   `json:"confirm"`
	Method  string `json:"method"` // "request" (default) or "cancel"
}

// SendInvitations emails the stored event's attendees. The invitation is
// built from the stored row (UID and SEQUENCE come from storage, never from
// the request body) and rate limited to one burst per event version.
// POST /mailbox/calendars/events/:id/invitations
func (h MailboxCalendarHandler) SendInvitations(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	var req inviteRequest
	if !bindMailboxMessage(c, &req) {
		return
	}
	method := collab.MethodRequest
	if req.Method != "" {
		switch strings.ToUpper(req.Method) {
		case collab.MethodRequest:
			method = collab.MethodRequest
		case collab.MethodCancel:
			method = collab.MethodCancel
		default:
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
			return
		}
	}
	row, err := h.Service.Q.PlatformGetEvent(c.Request.Context(), store.PlatformGetEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusNotFound, "EVENT_NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	emails, status := appcollab.DecodeAttendees(row.Attendees)
	event, normErr := collab.NormalizeEvent(collab.Event{
		Summary: row.Summary, Description: row.Description, Location: row.Location,
		StartsAt: row.StartsAt.Time, EndsAt: row.EndsAt.Time, AllDay: row.AllDay,
		Timezone: row.Timezone, RRule: row.Rrule, Status: row.Status,
		Attendees: emails, AttendeeStatus: status,
	})
	if normErr != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.Address == "" {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	sequence := int(row.Sequence)
	if sequence <= 0 {
		sequence = int(row.Version) - 1
	}
	invitation := collab.Invitation{
		UID: row.Uid, Sequence: sequence, Method: method, Organizer: session.Address, Event: event,
	}
	if !defaultInvitationGate.allow(row.Uid, int64(sequence), method) {
		authError(c, http.StatusConflict, "INVITATION_ALREADY_SENT")
		return
	}
	sender, senderOK := h.invitationSender(c)
	if !senderOK {
		authError(c, http.StatusServiceUnavailable, "WEBMAIL_DISABLED")
		return
	}
	if err := collab.SendInvitations(c.Request.Context(), invitation, req.Confirm, sender); err != nil {
		defaultInvitationGate.release(row.Uid, int64(sequence), method)
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"queued": len(event.Attendees), "sequence": sequence, "method": method})
}
