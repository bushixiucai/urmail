package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// ListGroupMembers lists only contacts in an owned live group.
func (h MailboxContactsHandler) ListGroupMembers(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	q, ok := h.Service.Q.(*store.Queries)
	if !ok {
		authError(c, 503, "CONTACTS_UNAVAILABLE")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if _, err = q.PlatformGetContactGroup(c, store.PlatformGetContactGroupParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id}); err != nil {
		authError(c, 404, "CONTACT_GROUP_NOT_FOUND")
		return
	}
	rows, err := q.PlatformListContactGroupMembers(c, store.PlatformListContactGroupMembersParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id})
	if err != nil {
		authError(c, 503, "CONTACTS_UNAVAILABLE")
		return
	}
	c.JSON(200, gin.H{"contacts": contactViews(rows)})
}

// RemoveGroupMember removes a scoped membership without deleting the contact.
func (h MailboxContactsHandler) RemoveGroupMember(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	q, ok := h.Service.Q.(*store.Queries)
	if !ok {
		authError(c, 503, "CONTACTS_UNAVAILABLE")
		return
	}
	group, err := strconv.ParseInt(c.Param("id"), 10, 64)
	contact, e := strconv.ParseInt(c.Param("contact"), 10, 64)
	if err != nil || e != nil || group <= 0 || contact <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if _, err = q.PlatformGetContactGroup(c, store.PlatformGetContactGroupParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: group}); err != nil {
		authError(c, 404, "CONTACT_GROUP_NOT_FOUND")
		return
	}
	if _, err = q.PlatformRemoveContactGroupMember(c, store.PlatformRemoveContactGroupMemberParams{TenantID: p.TenantID, MailboxID: p.MailboxID, GroupID: group, ContactID: contact}); err != nil {
		authError(c, 503, "CONTACTS_UNAVAILABLE")
		return
	}
	c.Status(http.StatusNoContent)
}

// occurrenceEdit applies an exception under the original series version. The
// single SQL update changes the series, its override snapshot and change log.
func (h MailboxCalendarHandler) occurrenceEdit(c *gin.Context, p appcollab.Principal, id, version int64, recurrenceID string, child collab.Event) (collab.Event, bool) {
	row, err := h.Service.Q.PlatformGetEvent(c, store.PlatformGetEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id})
	if err != nil || row.Version != version {
		authError(c, http.StatusConflict, "EVENT_CONFLICT")
		return collab.Event{}, false
	}
	event, _, err := appcollab.EventFromRow(row)
	if err != nil {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return collab.Event{}, false
	}
	stamp, err := time.Parse(time.RFC3339, recurrenceID)
	if err != nil || event.RRule == "" {
		authError(c, 400, "INVALID_PARAMS")
		return collab.Event{}, false
	}
	if child.Summary == "" {
		child = collab.InstanceAt(event, stamp)
		child.Status = "cancelled" //nolint:misspell // RFC 5545 STATUS spelling.
	} // RFC 5545 STATUS spelling.
	override := collab.OccurrenceOverride{RecurrenceID: stamp, Summary: child.Summary, Description: child.Description, Location: child.Location, StartsAt: child.StartsAt, EndsAt: child.EndsAt, AllDay: child.AllDay, Status: child.Status}
	next := make([]collab.OccurrenceOverride, 0, len(event.Overrides)+1)
	for _, existing := range event.Overrides {
		if !existing.RecurrenceID.Equal(stamp) {
			next = append(next, existing)
		}
	}
	next = append(next, override)
	event.Overrides = next
	event, err = collab.NormalizeEvent(event)
	if err != nil {
		authError(c, 400, "INVALID_PARAMS")
		return collab.Event{}, false
	}
	return event, true
}

func (h MailboxCalendarHandler) deleteOccurrence(c *gin.Context, p appcollab.Principal, id, version int64) {
	event, ok := h.occurrenceEdit(c, p, id, version, c.Query("recurrence_id"), collab.Event{})
	if !ok {
		return
	}
	if _, err := h.Service.UpdateEvent(c, p, id, version, event); err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, appcollab.ErrConflict) {
			authError(c, 409, "EVENT_CONFLICT")
		} else {
			authError(c, 503, "CALENDAR_UNAVAILABLE")
		}
		return
	}
	c.JSON(200, gin.H{"deleted": true, "series_preserved": true})
}

type mailboxContactView struct {
	store.MailboxContacts
	Emails []collab.Email `json:"emails"`
	Phones []collab.Phone `json:"phones"`
}

func contactView(row store.MailboxContacts) mailboxContactView {
	result := mailboxContactView{MailboxContacts: row, Emails: []collab.Email{}, Phones: []collab.Phone{}}
	_ = json.Unmarshal(row.Emails, &result.Emails)
	_ = json.Unmarshal(row.Phones, &result.Phones)
	if result.Emails == nil {
		result.Emails = []collab.Email{}
	}
	if result.Phones == nil {
		result.Phones = []collab.Phone{}
	}
	return result
}
func contactViews(rows []store.MailboxContacts) []mailboxContactView {
	out := make([]mailboxContactView, 0, len(rows))
	for _, row := range rows {
		out = append(out, contactView(row))
	}
	return out
}
