package handlers

import (
	"encoding/json"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// NotificationHandler exposes only scoped notices and operator alert facts.
type NotificationHandler struct{ Store *store.Queries }

// List returns only notifications visible to the authenticated tenant user.
func (h *NotificationHandler) List(c *gin.Context) {
	tid, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	cursor := int64(0)
	if raw := c.Query("before"); raw != "" {
		var err error
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 1 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	q := requestStore(c, h.Store)
	if q == nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	rows, err := q.ListTenantNotifications(c, store.ListTenantNotificationsParams{TenantID: pgtype.Int8{Int64: tid, Valid: true}, BeforeID: cursor})
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, gin.H{"id": row.ID, "kind": row.Kind, "message_key": row.MessageKey, "params": json.RawMessage(row.Params), "channel": row.Channel, "state": row.State, "created_at": row.CreatedAt, "read_at": row.ReadAt})
	}
	c.JSON(200, gin.H{"notifications": out})
}

// Alerts lists durable operational incidents for platform operators.
func (h *NotificationHandler) Alerts(c *gin.Context) {
	q := requestStore(c, h.Store)
	if q == nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	rows, err := q.PlatformListOperationalAlerts(c)
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"alerts": rows})
}
