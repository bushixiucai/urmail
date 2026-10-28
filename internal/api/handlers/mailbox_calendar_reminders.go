package handlers

import (
	"encoding/json"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/store"
)

// RegisterCalendarReminderRoutes exposes only the authenticated mailbox's
// persisted DISPLAY notifications and explicit acknowledgment.
func RegisterCalendarReminderRoutes(group *gin.RouterGroup, h MailboxCalendarHandler) {
	group.GET("/calendars/reminders", h.listReminders)
	group.POST("/calendars/reminders/:reminder_id/ack", h.acknowledgeReminder)
}
func (h MailboxCalendarHandler) listReminders(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	if h.Pool == nil {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return
	}
	rows, err := store.New(h.Pool).PlatformListCalendarReminders(c.Request.Context(), store.PlatformListCalendarRemindersParams{TenantID: p.TenantID, MailboxID: p.MailboxID})
	if err != nil {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, gin.H{"id": row.ID, "payload": json.RawMessage(row.Payload), "created_at": row.CreatedAt})
	}
	c.JSON(200, gin.H{"reminders": out})
}
func (h MailboxCalendarHandler) acknowledgeReminder(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("reminder_id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if h.Pool == nil {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return
	}
	n, err := store.New(h.Pool).PlatformAcknowledgeCalendarReminder(c.Request.Context(), store.PlatformAcknowledgeCalendarReminderParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id})
	if err != nil {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return
	}
	if n == 0 {
		authError(c, 404, "EVENT_NOT_FOUND")
		return
	}
	c.JSON(200, gin.H{"acknowledged": true})
}
