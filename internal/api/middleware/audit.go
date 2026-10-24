package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/store"
)

// AuditWriter is the trusted platform-side sink. Tenant roles cannot insert
// arbitrary audit rows; successful mutations use a scoped database function.
type AuditWriter interface {
	CreateAuditLog(context.Context, store.CreateAuditLogParams) (store.AuditLogs, error)
}

const contextAuditState = "urmail.audit_state"

type requestAudit struct {
	writer            AuditWriter
	logger            *zap.Logger
	recorded          bool
	preparedStatus    int
	phase             string
	forcedStatus      int
	businessCommitted bool
}

// Audit observes mutations, including authentication and CSRF rejection. Place
// it before authentication: only verified context populated by RequireAuth is
// used, and rejected unauthenticated input never supplies an audit tenant.
func Audit(writer AuditWriter, logger *zap.Logger) gin.HandlerFunc {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet:
			if !sensitiveRead(c.FullPath()) {
				c.Next()
				return
			}
		case http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if writer == nil {
			c.Next()
			return
		}
		state := &requestAudit{writer: writer, logger: logger}
		c.Set(contextAuditState, state)
		defer func() {
			if recovered := recover(); recovered != nil {
				raw, active := c.Get(contextTransaction)
				if !state.recorded && (!active || raw == nil) {
					state.forcedStatus = 500
					state.phase = "panic"
					finishIndependentAudit(c)
				}
				panic(recovered)
			}
		}()
		c.Next()
		// RequireTenantTx owns finalization while its transaction is active.
		if raw, ok := c.Get(contextTransaction); ok && raw != nil {
			return
		}
		finishIndependentAudit(c)
	}
}

func auditState(c *gin.Context) *requestAudit {
	v, exists := c.Get(contextAuditState)
	if !exists {
		return nil
	}
	state, _ := v.(*requestAudit)
	return state
}

func auditRecord(c *gin.Context) store.CreateAuditLogParams {
	var actorIP *netip.Addr
	if address, err := netip.ParseAddr(c.ClientIP()); err == nil {
		actorIP = &address
	}
	tenantID, tenantOK := GetTenantID(c)
	userID, userOK := GetUserID(c)
	// Never attribute an unauthenticated request to a caller-supplied tenant.
	if !userOK || userID <= 0 {
		tenantOK = false
	}
	phase := "request"
	if state := auditState(c); state != nil && state.phase != "" {
		phase = state.phase
	}
	status := c.Writer.Status()
	if state := auditState(c); state != nil && state.forcedStatus != 0 {
		status = state.forcedStatus
	}
	detail, _ := json.Marshal(map[string]any{"status": status, "outcome": auditOutcome(status), "phase": phase})
	return store.CreateAuditLogParams{
		TenantID:    pgtype.Int8{Int64: tenantID, Valid: tenantOK},
		ActorUserID: pgtype.Int8{Int64: userID, Valid: userOK && userID > 0},
		ActorIp:     actorIP, Action: c.Request.Method + " " + c.FullPath(),
		TargetType: pgtype.Text{String: "http", Valid: true},
		TargetID:   pgtype.Text{String: c.Request.URL.Path, Valid: true}, Detail: detail,
	}
}

// prepareTransactionAudit is called after handlers and before commit. The
// audit row shares the successful business transaction and cannot outlive a
// failed commit. Failed business transactions use the independent sink later.
func prepareTransactionAudit(c *gin.Context, tx pgx.Tx) error {
	if auditState(c) == nil {
		return nil
	}
	record := auditRecord(c)
	status := c.Writer.Status()
	if status < 100 || status > 599 {
		return errors.New("invalid HTTP audit status")
	}
	auditState(c).preparedStatus = status
	return store.New(tx).RecordTenantAudit(c.Request.Context(), store.RecordTenantAuditParams{
		TenantID: record.TenantID.Int64, UserID: record.ActorUserID.Int64,
		ActorIp: record.ActorIp, Action: record.Action, Target: record.TargetID.String,
		Status: int32(status),
	})
}

// finishIndependentAudit runs only after rollback/commit has released the
// request transaction. A canceled HTTP context must not cancel failure audit.
func finishIndependentAudit(c *gin.Context) {
	state := auditState(c)
	if state == nil || state.recorded {
		return
	}
	state.recorded = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := state.writer.CreateAuditLog(ctx, auditRecord(c)); err != nil {
		RecordAuditFailure(false)
		state.logger.Error("write independent audit log", zap.Error(err), zap.String("action", c.Request.Method+" "+c.FullPath()))
	}
}

func auditOutcome(status int) string {
	if status >= 400 {
		return "failed"
	}
	return "succeeded"
}

// Sensitive archive and financial reads need failure audits in addition to the domain
// READ audit. The route template contains no search term or message content.
func sensitiveRead(route string) bool {
	if route == "/api/v1/billing/invoices/:id/pdf" {
		return true
	}
	if !strings.HasPrefix(route, "/api/v1/archive/") {
		return false
	}
	return route == "/api/v1/archive/search" || strings.HasSuffix(route, "/content") || strings.HasSuffix(route, "/export") || strings.Contains(route, "/attachments/")
}
