package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// AuditStore reads append-only audit records in the tenant transaction.
type AuditStore interface {
	ListAuditLogsByTenant(ctx context.Context, arg store.ListAuditLogsByTenantParams) ([]store.AuditLogs, error)
	GetAuditCursorAnchor(context.Context, store.GetAuditCursorAnchorParams) (pgtype.Timestamptz, error)
	ExportAuditLogsByTenant(context.Context, store.ExportAuditLogsByTenantParams) ([]store.AuditLogs, error)
	RecordTenantAudit(context.Context, store.RecordTenantAuditParams) error
}

// AuditHandler serves tenant audit history.
type AuditHandler struct {
	Store AuditStore
}

// NewAuditHandler binds the audit query dependency.
func NewAuditHandler(s AuditStore) *AuditHandler {
	return &AuditHandler{Store: s}
}

// List returns the current tenant's audit records. Time range, actor, action
// and keyset filters are optional. Opaque cursors bind the complete ordering
// tuple to the tenant and filter scope. Legacy before_id uses its row timestamp.
func (h *AuditHandler) List(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	params := store.ListAuditLogsByTenantParams{
		TenantID: pgtype.Int8{Int64: tenantID, Valid: true},
		Limit:    100,
	}
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 32); err == nil && v > 0 && v <= 500 {
			params.Limit = int32(v)
		} else {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	if raw := c.Query("actor_user_id"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 {
			params.ActorUserID = pgtype.Int8{Int64: v, Valid: true}
		} else {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	if raw := c.Query("action"); raw != "" {
		params.Action = pgtype.Text{String: raw, Valid: true}
	}
	if raw := c.Query("from"); raw != "" {
		if t, err := parseTimeParam(raw); err == nil {
			params.FromTime = pgtype.Timestamptz{Time: t, Valid: true}
		} else {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	if raw := c.Query("to"); raw != "" {
		if t, err := parseTimeParam(raw); err == nil {
			params.ToTime = pgtype.Timestamptz{Time: t, Valid: true}
		} else {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	if raw := c.Query("before_id"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 {
			params.BeforeID = pgtype.Int8{Int64: v, Valid: true}
		} else {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	if params.FromTime.Valid && params.ToTime.Valid && !params.FromTime.Time.Before(params.ToTime.Time) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if raw := c.Query("target_type"); raw != "" {
		params.TargetType = pgtype.Text{String: raw, Valid: true}
	}
	if raw := c.Query("target_id"); raw != "" {
		params.TargetID = pgtype.Text{String: raw, Valid: true}
	}
	q := requestStore(c, h.Store)
	if export, _ := c.Get("urmail.audit_export"); export == true {
		h.export(c, q, params)
		return
	}
	scope := auditCursorScope(tenantID, c.Query("actor_user_id"), c.Query("action"), c.Query("target_type"), c.Query("target_id"), c.Query("from"), c.Query("to"))
	if raw := c.Query("cursor"); raw != "" {
		if len(raw) > 2048 || c.Query("before_id") != "" {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
			return
		}
		payload, decodeErr := base64.RawURLEncoding.DecodeString(raw)
		var cursor auditCursor
		if decodeErr != nil || json.Unmarshal(payload, &cursor) != nil || cursor.ID <= 0 || cursor.Time.IsZero() || cursor.Scope != scope {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
			return
		}
		anchor, anchorErr := q.GetAuditCursorAnchor(c, store.GetAuditCursorAnchorParams{TenantID: params.TenantID, ID: cursor.ID})
		if anchorErr != nil || !anchor.Valid || !anchor.Time.Equal(cursor.Time) {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		params.BeforeID = pgtype.Int8{Int64: cursor.ID, Valid: true}
		params.BeforeTime = pgtype.Timestamptz{Time: cursor.Time, Valid: true}
	}
	logs, err := requestStore(c, h.Store).ListAuditLogsByTenant(c, params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	next := ""
	if len(logs) == int(params.Limit) {
		last := logs[len(logs)-1]
		if last.CreatedAt.Valid {
			payload, _ := json.Marshal(auditCursor{ID: last.ID, Time: last.CreatedAt.Time.UTC(), Scope: scope})
			next = base64.RawURLEncoding.EncodeToString(payload)
		}
	}
	c.JSON(http.StatusOK, gin.H{"logs": logs, "next_cursor": next})
}

type auditCursor struct {
	ID    int64     `json:"id"`
	Time  time.Time `json:"time"`
	Scope string    `json:"scope"`
}

func auditCursorScope(tenantID int64, filters ...string) string {
	payload, _ := json.Marshal(append([]string{strconv.FormatInt(tenantID, 10)}, filters...))
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// Export returns one consistent, tenant-scoped metadata snapshot. Oversized
// results are rejected before any content is released, never silently truncated.
func (h *AuditHandler) Export(c *gin.Context) {
	role := middleware.GetRole(c)
	if role != "owner" && role != "admin" {
		authError(c, 403, "FORBIDDEN_ROLE")
		return
	}
	c.Set("urmail.audit_export", true)
	h.List(c)
}

func (h *AuditHandler) export(c *gin.Context, q AuditStore, p store.ListAuditLogsByTenantParams) {
	if c.Query("cursor") != "" || c.Query("before_id") != "" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	rows, err := q.ExportAuditLogsByTenant(c, store.ExportAuditLogsByTenantParams{TenantID: p.TenantID, ActorUserID: p.ActorUserID, Action: p.Action, TargetType: p.TargetType, TargetID: p.TargetID, FromTime: p.FromTime, ToTime: p.ToTime})
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if len(rows) > 10000 {
		authError(c, 413, "AUDIT_EXPORT_TOO_LARGE")
		return
	}
	var body bytes.Buffer
	csvWriter := csv.NewWriter(&body)
	_ = csvWriter.Write([]string{"id", "created_at", "actor_user_id", "action", "target_type", "target_id", "status", "outcome"})
	for _, row := range rows {
		var detail struct {
			Status  int    `json:"status"`
			Outcome string `json:"outcome"`
		}
		_ = json.Unmarshal(row.Detail, &detail)
		_ = csvWriter.Write([]string{strconv.FormatInt(row.ID, 10), row.CreatedAt.Time.UTC().Format(time.RFC3339Nano), strconv.FormatInt(row.ActorUserID.Int64, 10), auditCSVCell(row.Action), auditCSVCell(row.TargetType.String), auditCSVCell(row.TargetID.String), strconv.Itoa(detail.Status), auditCSVCell(detail.Outcome)})
	}
	csvWriter.Flush()
	if csvWriter.Error() != nil || body.Len() > 7<<20 {
		authError(c, 413, "AUDIT_EXPORT_TOO_LARGE")
		return
	}
	tid, _ := middleware.GetTenantID(c)
	uid, _ := middleware.GetUserID(c)
	if err = q.RecordTenantAudit(c, store.RecordTenantAuditParams{TenantID: tid, UserID: uid, Action: "GET " + c.FullPath(), Target: c.Request.URL.Path, Status: 200}); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.Header("Content-Disposition", `attachment; filename="audit.csv"`)
	c.Data(200, "text/csv; charset=utf-8", body.Bytes())
}

func auditCSVCell(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if strings.ContainsAny(trimmed[:min(len(trimmed), 1)], "=+-@") {
		return "'" + value
	}
	return value
}
