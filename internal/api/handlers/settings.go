package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

type settingsPatch struct {
	Settings map[string]json.RawMessage `json:"settings" binding:"required"`
}

var settingColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func validateSettings(values map[string]json.RawMessage) bool {
	if len(values) > 3 {
		return false
	}
	for key, value := range values {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		switch key {
		case "locale":
			var s string
			if json.Unmarshal(value, &s) != nil || (s != "en" && s != "zh-CN") {
				return false
			}
		case "mailbox_default_quota_bytes":
			var n int64
			if json.Unmarshal(value, &n) != nil || n <= 0 {
				return false
			}
		case "theme":
			var theme map[string]string
			if json.Unmarshal(value, &theme) != nil || len(theme) != 1 || !settingColor.MatchString(theme["primary"]) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func settingKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type tenantSettingsStore interface {
	UpsertTenantSetting(context.Context, store.UpsertTenantSettingParams) (store.Settings, error)
	DeleteTenantSetting(context.Context, store.DeleteTenantSettingParams) error
}

// UpdateSettings updates allowlisted tenant overrides; null restores inheritance.
func (h *TenantHandler) UpdateSettings(c *gin.Context) {
	tid, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(401, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	var input settingsPatch
	if c.ShouldBindJSON(&input) != nil || !validateSettings(input.Settings) {
		c.JSON(400, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	q := requestStore[tenantSettingsStore](c, nil)
	if q == nil {
		c.JSON(503, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	for _, key := range settingKeys(input.Settings) {
		value := input.Settings[key]
		var err error
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			err = q.DeleteTenantSetting(c, store.DeleteTenantSettingParams{ScopeID: tid, Key: key})
		} else {
			_, err = q.UpsertTenantSetting(c, store.UpsertTenantSettingParams{ScopeID: tid, Key: key, Value: value})
		}
		if err != nil {
			c.JSON(500, gin.H{"error": gin.H{"code": "DB_ERROR"}})
			return
		}
	}
	h.Current(c)
}

// Settings returns platform-level editable defaults.
func (h *PlatformHandler) Settings(c *gin.Context) {
	if h.Pool == nil {
		c.JSON(503, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	rows, err := store.New(h.Pool.Pool).PlatformListSettings(c)
	if err != nil {
		c.JSON(500, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	values := map[string]json.RawMessage{}
	for _, row := range rows {
		values[row.Key] = row.Value
	}
	c.JSON(200, gin.H{"settings": values})
}

// UpdateSettings atomically updates allowlisted platform defaults.
func (h *PlatformHandler) UpdateSettings(c *gin.Context) {
	var input settingsPatch
	if c.ShouldBindJSON(&input) != nil || !validateSettings(input.Settings) {
		c.JSON(400, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	if h.Pool == nil {
		c.JSON(503, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		c.JSON(500, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	for _, key := range settingKeys(input.Settings) {
		value := input.Settings[key]
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			err = q.PlatformDeleteSetting(c, key)
		} else {
			err = q.PlatformUpsertSetting(c, store.PlatformUpsertSettingParams{Key: key, Value: value})
		}
		if err != nil {
			c.JSON(500, gin.H{"error": gin.H{"code": "DB_ERROR"}})
			return
		}
	}
	if err = tx.Commit(c); err != nil {
		c.JSON(500, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	h.Settings(c)
}
