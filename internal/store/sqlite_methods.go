// Package store — SQLite 方言手写层：Phase 1 引擎迁移期间从 sqlc 生成中
// 拆出的语句（S2 B1b）在此承接，直至 B5 引擎切换；替代物逐条见
// scripts/sqlite/dfn-calls.json。
package store

// scope 映射（S3 decorator 消费；method_scopes.json 的手写层补充）：
//   PlatformRevokeMailboxSessions            → system（mailbox 凭据撤销，worker/管理员触发）
//   PlatformConsumeMailboxOperationTickets   → system（工单消费，同上）
//   PlatformApplyArchiveRetentionCandidates  → system（保留策略执行，后台 worker）
//   PlatformApplyArchiveRetentionMarkContent → system（同上）
//   PlatformApplyArchiveRetentionMarkDeleted → system（同上）
//   PlatformRecordMarketingFlowNodeExecution → system（marketing flow worker 节点执行）
//   PlatformBumpMarketingFlowAttempt         → system（marketing flow worker 失败退避）

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

const revokeMailboxSessionsSQL = `UPDATE mailbox_sessions
SET revoked_at=COALESCE(revoked_at, (CAST(strftime('%s','now') AS INTEGER)*1000000))
WHERE tenant_id=$1 AND mailbox_id=$2`

const consumeMailboxOperationTicketsSQL = `UPDATE mailbox_operation_tickets
SET consumed_at=COALESCE(consumed_at, (CAST(strftime('%s','now') AS INTEGER)*1000000))
WHERE tenant_id=$1 AND mailbox_id=$2`

// PlatformRevokeMailboxSessionsParams is the parameter set for
// PlatformRevokeMailboxSessions.
type PlatformRevokeMailboxSessionsParams struct {
	TenantID  int64
	MailboxID int64
}

// PlatformRevokeMailboxSessions revokes every session of one mailbox.
func (q *Queries) PlatformRevokeMailboxSessions(ctx context.Context, arg PlatformRevokeMailboxSessionsParams) (int64, error) {
	tag, err := q.db.Exec(ctx, revokeMailboxSessionsSQL, arg.TenantID, arg.MailboxID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// PlatformConsumeMailboxOperationTicketsParams is the parameter set for
// PlatformConsumeMailboxOperationTickets.
type PlatformConsumeMailboxOperationTicketsParams struct {
	TenantID  int64
	MailboxID int64
}

// PlatformConsumeMailboxOperationTickets consumes pending operation tickets of
// one mailbox.
func (q *Queries) PlatformConsumeMailboxOperationTickets(ctx context.Context, arg PlatformConsumeMailboxOperationTicketsParams) (int64, error) {
	tag, err := q.db.Exec(ctx, consumeMailboxOperationTicketsSQL, arg.TenantID, arg.MailboxID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const archiveRetentionCandidatesSQL = `SELECT a.id, a.tenant_id, p.confirmed_preview FROM archive_entries a
 JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
 JOIN retention_policies p ON p.tenant_id=a.tenant_id
 WHERE p.active AND p.action='soft_delete' AND p.confirmed_preview IS NOT NULL
  AND (SELECT COALESCE((SELECT 1 FROM compliance_grants cg JOIN user_tenant_roles ur ON ur.tenant_id=cg.tenant_id AND ur.user_id=cg.user_id
       WHERE cg.tenant_id=p.tenant_id AND cg.user_id=p.created_by AND cg.scope='retention_manage' AND NOT cg.revoked
        AND cg.owner_confirmed_at IS NOT NULL AND ur.role IN('owner','admin')), 0))=1
  AND NOT a.legal_hold AND a.deleted_at IS NULL
  AND a.received_at < (CAST(strftime('%s','now') AS INTEGER)*1000000) - p.retention_days*86400000000
 ORDER BY a.id LIMIT 100`

// PlatformApplyArchiveRetentionCandidatesRow is one retention
// soft-delete candidate entry.
type PlatformApplyArchiveRetentionCandidatesRow struct {
	ID               int64
	TenantID         int64
	ConfirmedPreview pgtype.UUID
}

// PlatformApplyArchiveRetentionCandidates lists entries eligible for
// soft-delete under an authorized retention policy (B1b split of the former
// DML-CTE chain).
func (q *Queries) PlatformApplyArchiveRetentionCandidates(ctx context.Context) ([]PlatformApplyArchiveRetentionCandidatesRow, error) {
	rows, err := q.db.Query(ctx, archiveRetentionCandidatesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []PlatformApplyArchiveRetentionCandidatesRow
	for rows.Next() {
		var i PlatformApplyArchiveRetentionCandidatesRow
		if err := rows.Scan(&i.ID, &i.TenantID, &i.ConfirmedPreview); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

const archiveRetentionMarkContentSQL = `UPDATE archive_content SET retention_preview=(
 SELECT confirmed_preview FROM retention_policies p WHERE p.tenant_id=archive_content.tenant_id AND p.active AND p.action='soft_delete')
WHERE (entry_id, tenant_id) IN (` + archiveRetentionCandidatesSQL + `)`

// PlatformApplyArchiveRetentionMarkContent stamps the retention preview onto
// the candidate archive content rows. MUST run BEFORE MarkDeleted (the
// candidate predicate requires deleted_at IS NULL).
func (q *Queries) PlatformApplyArchiveRetentionMarkContent(ctx context.Context) (int64, error) {
	tag, err := q.db.Exec(ctx, archiveRetentionMarkContentSQL)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const archiveRetentionMarkDeletedSQL = `UPDATE archive_entries SET deleted_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE (id, tenant_id) IN (` + archiveRetentionCandidatesSQL + `)`

// PlatformApplyArchiveRetentionMarkDeleted soft-deletes the candidate entries.
// MUST run AFTER MarkContent (ordering: content preview first, then delete).
func (q *Queries) PlatformApplyArchiveRetentionMarkDeleted(ctx context.Context) (int64, error) {
	tag, err := q.db.Exec(ctx, archiveRetentionMarkDeletedSQL)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const recordMarketingFlowNodeExecutionSQL = `INSERT INTO marketing_flow_node_executions(tenant_id, position_id, lease_fence, node_key, status, error_code)
SELECT p.tenant_id, p.id, p.lease_fence, $4, $5, $6
FROM marketing_flow_positions p
WHERE p.tenant_id=$1 AND p.flow_id=$2 AND p.email=$3
 AND p.lease_owner=$7 AND p.lease_fence=$8 AND p.lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000)
 AND EXISTS(SELECT 1 FROM marketing_flow_versions v WHERE v.tenant_id=p.tenant_id AND v.runtime_flow_id=p.flow_id)
ON CONFLICT(tenant_id, position_id, lease_fence) DO NOTHING`

// PlatformRecordMarketingFlowNodeExecutionParams is the parameter set for
// PlatformRecordMarketingFlowNodeExecution.
type PlatformRecordMarketingFlowNodeExecutionParams struct {
	TenantID   int64
	FlowID     int64
	Email      string
	LeaseOwner string
	LeaseFence int64
	NodeKey    string
	Status     string
	ErrorCode  string
}

// PlatformRecordMarketingFlowNodeExecution records one node execution outcome
// under a live lease (no-op when the fence/lease no longer matches).
func (q *Queries) PlatformRecordMarketingFlowNodeExecution(ctx context.Context, arg PlatformRecordMarketingFlowNodeExecutionParams) (int64, error) {
	tag, err := q.db.Exec(ctx, recordMarketingFlowNodeExecutionSQL,
		arg.TenantID, arg.FlowID, arg.Email, arg.NodeKey, arg.Status, arg.ErrorCode,
		arg.LeaseOwner, arg.LeaseFence)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const bumpMarketingFlowAttemptSQL = `UPDATE marketing_flow_positions
SET attempt_count=attempt_count+1,
 last_error=$6,
 next_attempt_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) + $7*1000000
WHERE tenant_id=$1 AND flow_id=$2 AND email=$3
 AND lease_owner=$4 AND lease_fence=$5`

// PlatformBumpMarketingFlowAttemptParams is the parameter set for
// PlatformBumpMarketingFlowAttempt.
type PlatformBumpMarketingFlowAttemptParams struct {
	TenantID       int64
	FlowID         int64
	Email          string
	LeaseOwner     string
	LeaseFence     int64
	ErrorCode      string
	BackoffSeconds int64
}

// PlatformBumpMarketingFlowAttempt bumps the attempt counter and schedules the
// retry backoff (failure path of the flow worker).
func (q *Queries) PlatformBumpMarketingFlowAttempt(ctx context.Context, arg PlatformBumpMarketingFlowAttemptParams) (int64, error) {
	tag, err := q.db.Exec(ctx, bumpMarketingFlowAttemptSQL,
		arg.TenantID, arg.FlowID, arg.Email, arg.LeaseOwner, arg.LeaseFence,
		arg.ErrorCode, arg.BackoffSeconds)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const getUserMFARecoveryCodesSQL = `SELECT mfa_recovery_codes FROM users WHERE id = ?1`

const setUserMFARecoveryCodesSQL = `UPDATE users SET mfa_recovery_codes = ?2 WHERE id = ?1`

// ConsumeUserRecoveryCode removes one recovery code (matched by its stored
// SHA-256 hex hash) from the user's code list. JSON array filtering happens
// in Go because sqlc's sqlite engine cannot resolve json_each columns.
// Returns the number of codes removed (0 or 1).
func (q *Queries) ConsumeUserRecoveryCode(ctx context.Context, userID int64, hash string) (int64, error) {
	var raw string
	if err := q.db.QueryRow(ctx, getUserMFARecoveryCodesSQL, userID).Scan(&raw); err != nil {
		return 0, err
	}
	var codes []string
	if err := json.Unmarshal([]byte(raw), &codes); err != nil {
		return 0, fmt.Errorf("parse mfa_recovery_codes: %w", err)
	}
	var kept []string
	found := false
	for _, c := range codes {
		if c == hash && !found {
			found = true
			continue
		}
		kept = append(kept, c)
	}
	if !found {
		return 0, nil
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return 0, fmt.Errorf("marshal filtered codes: %w", err)
	}
	tag, err := q.db.Exec(ctx, setUserMFARecoveryCodesSQL, userID, string(out))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

