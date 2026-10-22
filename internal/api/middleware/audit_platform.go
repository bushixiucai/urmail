package middleware

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/store"
)

// AuditPlatform records successful platform mutations. The tenant id is taken
// from a tenant-scoped path (:id) when present and is NULL for global actions
// such as brand/plan changes.
func AuditPlatform(writer AuditWriter, logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return
		}
		if writer == nil || c.Writer.Status() >= 500 {
			return
		}
		var tenantID pgtype.Int8
		if raw := c.Param("id"); raw != "" {
			if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
				tenantID = pgtype.Int8{Int64: id, Valid: true}
			}
		}
		userID, _ := GetUserID(c)
		var actorIP *netip.Addr
		if addr, err := netip.ParseAddr(c.ClientIP()); err == nil {
			actorIP = &addr
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		action := c.Request.Method + " " + c.FullPath()
		if _, err := writer.CreateAuditLog(ctx, store.CreateAuditLogParams{
			TenantID:    tenantID,
			ActorUserID: pgtype.Int8{Int64: userID, Valid: userID > 0},
			ActorIp:     actorIP,
			Action:      action,
			TargetType:  pgtype.Text{String: "platform", Valid: true},
			TargetID:    pgtype.Text{String: c.Request.URL.Path, Valid: true},
			Detail:      []byte("{}"),
		}); err != nil {
			logger.Warn("write platform audit log", zap.Error(err), zap.String("action", action))
		}
	}
}
