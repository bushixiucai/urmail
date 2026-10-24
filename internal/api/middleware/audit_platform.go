package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/store"
)

type auditTargetResolver interface {
	PlatformResolveAuditTargetTenant(context.Context, store.PlatformResolveAuditTargetTenantParams) (int64, error)
}

// AuditPlatform records platform mutations and their final HTTP outcome. Only
// recognized tenant-owned resources are resolved; brands and plans are global.
func AuditPlatform(writer AuditWriter, logger *zap.Logger) gin.HandlerFunc {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(c *gin.Context) {
		if writer == nil || c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}
		defer func() {
			recovered := recover()
			committed, _ := c.Get(contextPlatformAuditCommitted)
			prepared, _ := c.Get(contextPlatformAuditStatus)
			if committed != true || recovered != nil || prepared != c.Writer.Status() || c.Writer.Status() >= 400 {
				recordPlatformAudit(c, writer, logger, recovered != nil)
			}
			if recovered != nil {
				panic(recovered)
			}
		}()
		c.Next()
	}
}

func recordPlatformAudit(c *gin.Context, writer AuditWriter, logger *zap.Logger, panicked bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	record := platformAuditRecord(ctx, c, writer, logger)
	if panicked {
		record.Detail = platformAuditDetail(c, 500, "panic")
	}
	if committed, _ := c.Get(contextPlatformAuditCommitted); committed == true {
		status := c.Writer.Status()
		if panicked {
			status = 500
		}
		record.Detail = platformAuditDetail(c, status, "after_commit")
	}
	if _, err := writer.CreateAuditLog(ctx, record); err != nil {
		RecordAuditFailure(false)
		logger.Error("write platform audit log", zap.Error(err), zap.String("action", record.Action))
	}
}

func platformAuditRecord(ctx context.Context, c *gin.Context, writer AuditWriter, logger *zap.Logger) store.CreateAuditLogParams {
	record := auditRecord(c)
	record.TenantID = pgtype.Int8{}
	record.TargetType = pgtype.Text{String: "platform", Valid: true}
	record.Detail = platformAuditDetail(c, c.Writer.Status(), "")
	if _, active := c.Get(contextTransaction); active {
		status := c.Writer.Status()
		record.Detail = platformAuditDetail(c, status, "transaction")
	}
	if candidate, ok := c.Get("urmail.platform_audit_tenant"); ok {
		if id, valid := candidate.(int64); valid && id > 0 {
			if !record.TargetID.Valid {
				record.TargetID = pgtype.Text{String: strconv.FormatInt(id, 10), Valid: true}
			}
			if resolver, ok := writer.(auditTargetResolver); ok {
				if tenantID, err := resolver.PlatformResolveAuditTargetTenant(ctx, store.PlatformResolveAuditTargetTenantParams{Kind: "tenants", ID: id}); err == nil {
					record.TenantID = pgtype.Int8{Int64: tenantID, Valid: true}
					return record
				}
			}
		}
	}
	if _, authenticated := GetUserID(c); authenticated {
		parts := strings.Split(strings.Trim(c.FullPath(), "/"), "/")
		for _, target := range []struct{ param, kind string }{{"order_id", "orders"}, {"tenant_id", "tenants"}} {
			if value := c.Param(target.param); value != "" {
				id, e := strconv.ParseInt(value, 10, 64)
				if resolver, ok := writer.(auditTargetResolver); ok && e == nil {
					if tenantID, lookup := resolver.PlatformResolveAuditTargetTenant(ctx, store.PlatformResolveAuditTargetTenantParams{Kind: target.kind, ID: id}); lookup == nil {
						record.TenantID = pgtype.Int8{Int64: tenantID, Valid: true}
						return record
					}
				}
			}
		}

		if len(parts) >= 6 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "platform" && parts[3] == "support" && parts[4] == "tenants" && parts[5] == ":id" {
			id, e := strconv.ParseInt(c.Param("id"), 10, 64)
			if resolver, ok := writer.(auditTargetResolver); ok && e == nil {
				if tenantID, lookup := resolver.PlatformResolveAuditTargetTenant(ctx, store.PlatformResolveAuditTargetTenantParams{Kind: "tenants", ID: id}); lookup == nil {
					record.TenantID = pgtype.Int8{Int64: tenantID, Valid: true}
					return record
				}
			}
		}
		if len(parts) >= 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "platform" && parts[4] == ":id" {
			kind := parts[3]
			if kind == "tenants" || kind == "orders" || kind == "invoices" || kind == "refunds" || kind == "payment-reviews" {
				id, parseErr := strconv.ParseInt(c.Param("id"), 10, 64)
				if resolver, ok := writer.(auditTargetResolver); ok && parseErr == nil {
					if tenantID, err := resolver.PlatformResolveAuditTargetTenant(ctx, store.PlatformResolveAuditTargetTenantParams{Kind: kind, ID: id}); err == nil {
						record.TenantID = pgtype.Int8{Int64: tenantID, Valid: true}
					} else {
						logger.Warn("resolve audit target tenant", zap.Error(err))
					}
				}
			}
		}
	}

	return record
}

// SetPlatformAuditTenant binds a trusted, database-resolved target to a platform mutation.
func SetPlatformAuditTenant(c *gin.Context, tenantID int64) {
	if tenantID > 0 {
		c.Set("urmail.platform_audit_tenant", tenantID)
	}
}

// SetPlatformAuditDetail adds explicit, bounded business justification, never an HTTP body dump.
func SetPlatformAuditDetail(c *gin.Context, detail map[string]any) {
	c.Set("urmail.platform_audit_detail", detail)
}
func platformAuditDetail(c *gin.Context, status int, phase string) []byte {
	detail := map[string]any{}
	if raw, ok := c.Get("urmail.platform_audit_detail"); ok {
		if values, valid := raw.(map[string]any); valid {
			for key, value := range values {
				detail[key] = value
			}
		}
	}
	detail["status"] = status
	detail["outcome"] = auditOutcome(status)
	if phase != "" {
		detail["phase"] = phase
	}
	data, _ := json.Marshal(detail)
	return data
}
