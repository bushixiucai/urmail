package handlers

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/notify"
	"github.com/bushixiucai/urmail/internal/store"
)

// NotificationConfigHandler accepts platform-only write-only relay credentials.
type NotificationConfigHandler struct {
	Store   *store.Queries
	Secrets SecretsBox
}

// Save persists only encrypted relay configuration in the audited request transaction.
func (h *NotificationConfigHandler) Save(c *gin.Context) {
	if h.Secrets == nil {
		authError(c, 503, "NOTIFICATION_UNCONFIGURED")
		return
	}
	var cfg notify.SMTPConfig
	if c.ShouldBindJSON(&cfg) != nil || cfg.Validate() != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	raw, err := json.Marshal(cfg) //nolint:gosec // The relay credentials are immediately encrypted and are never returned.
	if err != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	cipher, err := h.Secrets.EncryptString(string(raw))
	if err != nil || !strings.HasPrefix(cipher, "enc:v1:") {
		authError(c, 500, "INTERNAL_ERROR")
		return
	}
	value, _ := json.Marshal(cipher)
	q := requestStore(c, h.Store)
	if err = q.PlatformUpsertSetting(c, store.PlatformUpsertSettingParams{Key: "notification_channel", Value: value}); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"enabled": cfg.Enabled, "state": "configured", "credentials_saved": true})
}
