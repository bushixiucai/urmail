package handlers

import (
	"encoding/json"
	"net/netip"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

type auditResponseDetail struct {
	Status  *int   `json:"status,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	Phase   string `json:"phase,omitempty"`
}

type auditResponse struct {
	ID          int64                `json:"id"`
	TenantID    pgtype.Int8          `json:"tenant_id"`
	ActorUserID pgtype.Int8          `json:"actor_user_id"`
	ActorIP     *netip.Addr          `json:"actor_ip"`
	Action      string               `json:"action"`
	TargetType  pgtype.Text          `json:"target_type"`
	TargetID    pgtype.Text          `json:"target_id"`
	Detail      *auditResponseDetail `json:"detail"`
	CreatedAt   pgtype.Timestamptz   `json:"created_at"`
}

// auditResponses returns only the list contract's outcome metadata as JSON,
// never a base64 blob or arbitrary business/debug payload from the audit row.
func auditResponses(rows []store.AuditLogs) []auditResponse {
	result := make([]auditResponse, 0, len(rows))
	for _, row := range rows {
		var detail auditResponseDetail
		var safe *auditResponseDetail
		if json.Unmarshal(row.Detail, &detail) == nil {
			if detail.Status != nil && (*detail.Status < 100 || *detail.Status > 599) {
				detail.Status = nil
			}
			if len(detail.Outcome) > 64 {
				detail.Outcome = ""
			}
			if len(detail.Phase) > 64 {
				detail.Phase = ""
			}
			if detail.Status != nil || detail.Outcome != "" || detail.Phase != "" {
				safe = &detail
			}
		}
		result = append(result, auditResponse{ID: row.ID, TenantID: row.TenantID, ActorUserID: row.ActorUserID, ActorIP: row.ActorIp, Action: row.Action, TargetType: row.TargetType, TargetID: row.TargetID, Detail: safe, CreatedAt: row.CreatedAt})
	}
	return result
}
