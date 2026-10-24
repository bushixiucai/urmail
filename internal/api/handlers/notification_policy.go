package handlers

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/store"
)

// ReminderPolicy returns only the public operational reminder schedule, without relay credentials.
func (h *NotificationConfigHandler) ReminderPolicy(c *gin.Context) {
	days := []int{7, 3, 1, 0}
	raw, err := requestStore(c, h.Store).PlatformGetSubscriptionNoticeDays(c)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		authError(c, 500, "DB_ERROR")
		return
	}
	if err == nil && (json.Unmarshal(raw, &days) != nil || days == nil) {
		authError(c, 409, "NOTIFICATION_POLICY_INVALID")
		return
	}
	c.JSON(200, gin.H{"days": days})
}

// SaveReminderPolicy validates a bounded list before the audited platform transaction commits.
func (h *NotificationConfigHandler) SaveReminderPolicy(c *gin.Context) {
	var body struct {
		Days *[]int `json:"days"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Days == nil || len(*body.Days) > 16 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	days := *body.Days
	for _, day := range days {
		if day < 0 || day > 365 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	slices.Sort(days)
	days = slices.Compact(days)
	if days == nil {
		days = []int{}
	}
	raw, err := json.Marshal(days)
	if err != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if err = requestStore(c, h.Store).PlatformUpsertSetting(c, store.PlatformUpsertSettingParams{Key: "subscription_notice_days", Value: raw}); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"days": days})
}
