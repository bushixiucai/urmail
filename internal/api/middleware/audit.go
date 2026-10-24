package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/store"
)

// AuditWriter is the platform-side audit sink. Audit rows live outside the
// tenant transaction and are never writable by the app role (migration 0008).
type AuditWriter interface {
	CreateAuditLog(ctx context.Context, arg store.CreateAuditLogParams) (store.AuditLogs, error)
}

// Audit records one row for every mutating tenant request, SUCCESSFUL OR NOT:
// a denied or failed mutation is exactly what a compliance reviewer needs to
// see, and M6 review D4 found the earlier early-return hid all of them. It
// deliberately records only metadata (method, route, actor, tenant, status):
// request bodies can contain passwords and message content.
func Audit(writer AuditWriter, logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return
		}
		if writer == nil {
			return
		}
		tenantID, ok := GetTenantID(c)
		if !ok {
			return
		}
		userID, _ := GetUserID(c)

		var actorIP *netip.Addr
		if addr, err := netip.ParseAddr(c.ClientIP()); err == nil {
			actorIP = &addr
		}

		action := c.Request.Method + " " + c.FullPath()
		detail, _ := json.Marshal(map[string]any{
			"status":  c.Writer.Status(),
			"outcome": auditOutcome(c.Writer.Status()),
		})
		if raw, ok := c.Get(contextTransaction); ok {
			tx := raw.(pgx.Tx)
			if _, err := tx.Exec(c.Request.Context(), "SELECT urmail_record_audit($1,$2,$3,$4,$5)", tenantID, userID, actorIP, action, c.Request.URL.Path); err != nil {
				logger.Warn("write tenant audit log", zap.Error(err))
				c.Set("urmail.transaction_failed", true)
			}
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := writer.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tenantID, Valid: true}, ActorUserID: pgtype.Int8{Int64: userID, Valid: userID > 0}, ActorIp: actorIP, Action: action, TargetType: pgtype.Text{String: "http", Valid: true}, TargetID: pgtype.Text{String: c.Request.URL.Path, Valid: true}, Detail: detail})
		if err != nil {
			logger.Warn("write audit log", zap.Error(err))
		}
	}
}

// auditOutcome classifies a response status for audit readers.
func auditOutcome(status int) string {
	if status >= 400 {
		return "failed"
	}
	return "succeeded"
}
