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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/timeutil"
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

// ---------------------------------------------------------------------------
// S4 Batch A — app-assume 函数承接（纯 SQL 谓词已内联到 queries/*.sql；
// 需要会话时区/多语句/JSON 聚合/哈希的在此手写）。
// 全部方法保留 sqlc 生成的同名签名，B5 重新生成后由本层承接。
// ---------------------------------------------------------------------------

// businessTimezoneSQL is the SQLite form of urmail_business_timezone:
// tenant setting wins over brand, then platform, default Asia/Shanghai.
const businessTimezoneSQL = `SELECT COALESCE(
 (SELECT json_extract(value,'$') FROM settings WHERE scope='tenant' AND scope_id=?1 AND key='business_timezone'),
 (SELECT json_extract(s.value,'$') FROM settings s JOIN tenants t ON t.brand_id=s.scope_id WHERE t.id=?1 AND s.scope='brand' AND s.key='business_timezone'),
 (SELECT json_extract(value,'$') FROM settings WHERE scope='platform' AND scope_id=0 AND key='business_timezone'),
 'Asia/Shanghai')`

// quotaDaySQL is the SQLite form of urmail_quota_day: the business-timezone
// calendar day used as the mailbox_usage.day key. SQLite has no timezone
// database, so the conversion happens in Go and the result is bound as a
// parameter. The PG function also took pg_advisory_xact_lock(tenant) to
// serialize concurrent quota writes; SQLite has a single writer, so the lock
// is unnecessary (registered app_pending in the S2/S3 audit — no session
// semantics are dropped, but the advisory lock has no SQLite equivalent).
func (q *Queries) quotaDay(ctx context.Context, tenantID int64) (string, error) {
	var zone string
	if err := q.db.QueryRow(ctx, businessTimezoneSQL, tenantID).Scan(&zone); err != nil {
		return "", err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return "", fmt.Errorf("INVALID_TIMEZONE: %s: %w", zone, err)
	}
	return time.Now().In(loc).Format("2006-01-02"), nil
}

// mailboxDailyScopePredicate mirrors urmail_mail_allowed + domain readiness
// for one mailbox address (m is the mailboxes alias).
const mailboxDailyScopePredicate = `m.status='active'
 AND EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=m.tenant_id AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready')))
 AND EXISTS(SELECT 1 FROM domains rd WHERE rd.tenant_id=m.tenant_id AND rd.id=m.domain_id AND rd.status='active' AND rd.verified_at IS NOT NULL AND rd.provisioning_state='ready')`

const mailboxDailySentSQL = `SELECT COALESCE(u.messages_sent, 0) AS messages_sent,
       m.id AS mailbox_id,
       m.tenant_id,
       COALESCE((
           SELECT json_extract(s.value, '$')
           FROM settings s
           WHERE s.key = 'mailbox_daily_send_limit'
             AND ((s.scope = 'tenant' AND s.scope_id = m.tenant_id) OR (s.scope = 'platform' AND s.scope_id = 0))
           ORDER BY CASE WHEN s.scope = 'tenant' THEN 0 ELSE 1 END
           LIMIT 1
       ), -1) AS daily_send_limit
FROM mailboxes m
LEFT JOIN mailbox_usage u ON m.id = u.mailbox_id AND u.day = ?2
WHERE m.address = ?1 AND ` + mailboxDailyScopePredicate

// GetMailboxDailySentRow is one mailbox's daily-sent counters and configured
// limit (same shape as the sqlc row).
type GetMailboxDailySentRow struct {
	MessagesSent   int32 `json:"messages_sent"`
	MailboxID      int64 `json:"mailbox_id"`
	TenantID       int64 `json:"tenant_id"`
	DailySendLimit int32 `json:"daily_send_limit"`
}

func (q *Queries) mailboxDailySent(ctx context.Context, address string, dest ...any) error {
	var tenantID int64
	if err := q.db.QueryRow(ctx,
		`SELECT m.tenant_id FROM mailboxes m WHERE m.address=?1 AND `+mailboxDailyScopePredicate, address).Scan(&tenantID); err != nil {
		return err
	}
	day, err := q.quotaDay(ctx, tenantID)
	if err != nil {
		return err
	}
	return q.db.QueryRow(ctx, mailboxDailySentSQL, address, day).Scan(dest...)
}

// GetMailboxDailySent returns today's sent count and the daily limit for a
// mailbox address (policy service, platform scope).
func (q *Queries) GetMailboxDailySent(ctx context.Context, address string) (GetMailboxDailySentRow, error) {
	var i GetMailboxDailySentRow
	err := q.mailboxDailySent(ctx, address, []any{&i.MessagesSent, &i.MailboxID, &i.TenantID, &i.DailySendLimit})
	return i, err
}

// GetMailboxDailySentForUpdateRow is the same shape as GetMailboxDailySentRow.
type GetMailboxDailySentForUpdateRow struct {
	MessagesSent   int32 `json:"messages_sent"`
	MailboxID      int64 `json:"mailbox_id"`
	TenantID       int64 `json:"tenant_id"`
	DailySendLimit int32 `json:"daily_send_limit"`
}

// GetMailboxDailySentForUpdate is the transaction-time read used before a
// legacy (non-atomic) quota decision. PG took FOR UPDATE OF m to serialize
// concurrent evaluators; SQLite serializes writers globally and the read is
// consistent inside the caller's transaction, so no row lock exists.
func (q *Queries) GetMailboxDailySentForUpdate(ctx context.Context, address string) (GetMailboxDailySentForUpdateRow, error) {
	var i GetMailboxDailySentForUpdateRow
	err := q.mailboxDailySent(ctx, address, []any{&i.MessagesSent, &i.MailboxID, &i.TenantID, &i.DailySendLimit})
	return i, err
}

const incrementMailboxDailySentSQL = `INSERT INTO mailbox_usage (mailbox_id, tenant_id, day, messages_sent)
VALUES (?1, ?2, ?3, 1)
ON CONFLICT (mailbox_id, day)
DO UPDATE SET messages_sent = mailbox_usage.messages_sent + 1
WHERE mailbox_usage.messages_sent < ?4
RETURNING messages_sent`

// IncrementMailboxDailySentParams is the parameter set for
// IncrementMailboxDailySent.
type IncrementMailboxDailySentParams struct {
	MailboxID    int64 `json:"mailbox_id"`
	TenantID     int64 `json:"tenant_id"`
	MessagesSent int32 `json:"messages_sent"`
}

// IncrementMailboxDailySent atomically advances the daily sent counter while
// it stays below the limit; returns pgx.ErrNoRows when the limit is reached.
func (q *Queries) IncrementMailboxDailySent(ctx context.Context, arg IncrementMailboxDailySentParams) (int32, error) {
	day, err := q.quotaDay(ctx, arg.TenantID)
	if err != nil {
		return 0, err
	}
	var messagesSent int32
	err = q.db.QueryRow(ctx, incrementMailboxDailySentSQL,
		arg.MailboxID, arg.TenantID, day, arg.MessagesSent).Scan(&messagesSent)
	return messagesSent, err
}

// settingLimitSQL mirrors urmail_setting_limit: tenant setting wins over the
// platform setting, missing means unlimited (-1).
const settingLimitSQL = `SELECT COALESCE(
 (SELECT CAST(json_extract(value,'$') AS INTEGER) FROM settings WHERE scope='tenant' AND scope_id=?1 AND key=?2),
 (SELECT CAST(json_extract(value,'$') AS INTEGER) FROM settings WHERE scope='platform' AND scope_id=0 AND key=?2),
 -1)`

const takeMailboxQuotaSQL = `INSERT INTO mailbox_usage (mailbox_id, tenant_id, day, messages_sent)
SELECT ?1, ?2, ?3, 1
WHERE ?4 > 0
 AND ?2 IN (SELECT mt.id FROM tenants mt WHERE mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready')))
 AND EXISTS(SELECT 1 FROM mailboxes m JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
  WHERE m.tenant_id=?2 AND m.id=?1 AND m.status='active' AND m.send_enabled AND d.status='active' AND d.provisioning_state='ready')
ON CONFLICT (mailbox_id, day) DO UPDATE SET messages_sent = mailbox_usage.messages_sent + 1
WHERE mailbox_usage.messages_sent < ?4
RETURNING messages_sent`

// PlatformTakeMailboxDailyQuotaParams is the parameter set for
// PlatformTakeMailboxDailyQuota.
type PlatformTakeMailboxDailyQuotaParams struct {
	TenantID  int64 `json:"tenant_id"`
	MailboxID int64 `json:"mailbox_id"`
}

// PlatformTakeMailboxDailyQuota is the atomic send-quota gate used by the
// SMTP policy service (mirrors urmail_take_mailbox_quota). The advisory lock
// is replaced by SQLite's single-writer serialization.
func (q *Queries) PlatformTakeMailboxDailyQuota(ctx context.Context, arg PlatformTakeMailboxDailyQuotaParams) (bool, error) {
	var limit int64
	if err := q.db.QueryRow(ctx, settingLimitSQL, arg.TenantID, "mailbox_daily_send_limit").Scan(&limit); err != nil {
		return false, err
	}
	if limit == 0 {
		return false, nil
	}
	if limit < 0 || limit > 2147483647 {
		limit = 2147483647
	}
	day, err := q.quotaDay(ctx, arg.TenantID)
	if err != nil {
		return false, err
	}
	var sent int32
	err = q.db.QueryRow(ctx, takeMailboxQuotaSQL, arg.MailboxID, arg.TenantID, day, limit).Scan(&sent)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

const recordTenantAuditInsertSQL = `INSERT INTO audit_logs(tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail)
SELECT ?1, ?2, ?3, ?4, 'http', ?5, ?6
WHERE EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=?1 AND user_id=?2)`

var auditActionPattern = regexp.MustCompile(`^[A-Za-z]{3,10} [A-Za-z0-9/{}:._~-]{0,120}$`)

// RecordTenantAuditParams is the parameter set for RecordTenantAudit.
type RecordTenantAuditParams struct {
	TenantID int64       `json:"tenant_id"`
	UserID   int64       `json:"user_id"`
	ActorIp  *netip.Addr `json:"actor_ip"`
	Action   string      `json:"action"`
	Target   string      `json:"target"`
	Status   int32       `json:"status"`
}

// RecordTenantAudit writes the tenant-visible audit row (mirrors
// urmail_record_audit: metadata validation + role membership). The PG
// function additionally required current_setting('urmail.tenant_id'/'urmail.user_id')
// to equal the arguments — that session binding is the S2 decorator's job and
// is registered app_pending (no role semantics are silently dropped here).
func (q *Queries) RecordTenantAudit(ctx context.Context, arg RecordTenantAuditParams) error {
	if arg.Action == "" || !auditActionPattern.MatchString(arg.Action) {
		return fmt.Errorf("invalid audit metadata: action %q", arg.Action)
	}
	if arg.Target == "" || len(arg.Target) > 2048 {
		return fmt.Errorf("invalid audit metadata: target length %d", len(arg.Target))
	}
	if arg.Status < 100 || arg.Status > 599 {
		return fmt.Errorf("invalid audit metadata: status %d", arg.Status)
	}
	outcome := "succeeded"
	if arg.Status >= 400 {
		outcome = "failed"
	}
	detail, err := json.Marshal(struct {
		Phase   string `json:"phase"`
		Status  int32  `json:"status"`
		Outcome string `json:"outcome"`
	}{"transaction", arg.Status, outcome})
	if err != nil {
		return err
	}
	var ip any
	if arg.ActorIp != nil {
		ip = arg.ActorIp.String()
	}
	var out int64
	return q.db.QueryRow(ctx, recordTenantAuditInsertSQL, arg.TenantID, arg.UserID, ip, arg.Action, arg.Target, string(detail)).Scan(&out)
}

const evidenceVisibleSQL = `SELECT EXISTS(
  SELECT 1 FROM archive_approval_requests r
  JOIN archive_investigation_members m ON m.tenant_id=r.tenant_id AND m.investigation_id=r.investigation_id
  WHERE r.tenant_id=?1 AND r.id=?2 AND r.state='approved' AND r.expires_at>?5
   AND r.evidence_digest=?4 AND m.user_id=?3 AND m.removed_at IS NULL)`

// PlatformReadEvidenceBody returns the approved evidence body when the
// manifest is visible and the stored digest matches (mirrors
// urmail_read_evidence_body). Session tenant/user binding is app_pending
// (S2 decorator).
func (q *Queries) PlatformReadEvidenceBody(ctx context.Context, arg PlatformReadEvidenceBodyParams) ([]byte, error) {
	var visible bool
	if err := q.db.QueryRow(ctx, evidenceVisibleSQL,
		arg.PTenant, arg.PRequest, arg.PActor, arg.PDigest, timeutil.NowMicro()).Scan(&visible); err != nil {
		return nil, err
	}
	if !visible {
		return nil, nil
	}
	var body []byte
	err := q.db.QueryRow(ctx,
		`SELECT evidence_body FROM archive_approval_requests WHERE tenant_id=?1 AND id=?2 AND evidence_digest=?3`,
		arg.PTenant, arg.PRequest, arg.PDigest).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != arg.PDigest {
		return nil, nil
	}
	return body, nil
}

const markMarketingFlowUncertainSQL = `UPDATE marketing_flow_positions
SET delivery_uncertain=true, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND flow_id=?2 AND email=?3`

// PlatformMarkMarketingFlowUncertain freezes a flow position after an
// ambiguous send (mirrors urmail_mark_marketing_flow_uncertain).
func (q *Queries) PlatformMarkMarketingFlowUncertain(ctx context.Context, arg PlatformMarkMarketingFlowUncertainParams) (string, error) {
	tag, err := q.db.Exec(ctx, markMarketingFlowUncertainSQL, arg.PTenant, arg.PFlow, arg.PEmail)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "missing", nil
	}
	return "uncertain", nil
}

const deleteContactGroupSQL = `UPDATE mailbox_contact_groups
SET deleted_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND deleted_at IS NULL`

const deleteContactGroupMembersSQL = `DELETE FROM mailbox_contact_group_members
WHERE tenant_id=?1 AND mailbox_id=?2 AND group_id=?3`

// PlatformDeleteContactGroup soft-deletes a contact group and clears its
// members (mirrors urmail_delete_contact_group; both writes always run).
func (q *Queries) PlatformDeleteContactGroup(ctx context.Context, arg PlatformDeleteContactGroupParams) (bool, error) {
	tag, err := q.db.Exec(ctx, deleteContactGroupSQL, arg.PTenant, arg.PMailbox, arg.PGroup)
	if err != nil {
		return false, err
	}
	if _, err := q.db.Exec(ctx, deleteContactGroupMembersSQL, arg.PTenant, arg.PMailbox, arg.PGroup); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// PlatformReadEvidenceBodyParams is the parameter set for
// PlatformReadEvidenceBody.
type PlatformReadEvidenceBodyParams struct {
	PTenant  int64  `json:"p_tenant"`
	PRequest int64  `json:"p_request"`
	PActor   int64  `json:"p_actor"`
	PDigest  string `json:"p_digest"`
}

// PlatformMarkMarketingFlowUncertainParams is the parameter set for
// PlatformMarkMarketingFlowUncertain.
type PlatformMarkMarketingFlowUncertainParams struct {
	PTenant int64  `json:"p_tenant"`
	PFlow   int64  `json:"p_flow"`
	PEmail  string `json:"p_email"`
}

// PlatformDeleteContactGroupParams is the parameter set for
// PlatformDeleteContactGroup.
type PlatformDeleteContactGroupParams struct {
	PTenant  int64 `json:"p_tenant"`
	PMailbox int64 `json:"p_mailbox"`
	PGroup   int64 `json:"p_group"`
}

// ArchiveCasesSnapshotParams is the parameter set for ArchiveCasesSnapshot.
type ArchiveCasesSnapshotParams struct {
	T int64 `json:"t"`
	C int64 `json:"c"`
	U int64 `json:"u"`
}

// ArchiveCasesSnapshotDigestParams is the parameter set for
// ArchiveCasesSnapshotDigest.
type ArchiveCasesSnapshotDigestParams struct {
	T int64 `json:"t"`
	C int64 `json:"c"`
	U int64 `json:"u"`
}

// ArchiveCasesExportVisibleParams is the parameter set for
// ArchiveCasesExportVisible.
type ArchiveCasesExportVisibleParams struct {
	T int64 `json:"t"`
	A int64 `json:"a"`
	U int64 `json:"u"`
}

const archiveCaseGrantSQL = `SELECT EXISTS(
 SELECT 1 FROM compliance_grants g
 JOIN user_tenant_roles r ON r.tenant_id=g.tenant_id AND r.user_id=g.user_id
 WHERE g.tenant_id=?1 AND g.user_id=?2 AND g.scope=?3 AND NOT g.revoked AND g.expires_at>?4
  AND g.owner_confirmed_at IS NOT NULL AND g.granted_by IS NOT NULL AND g.granted_by<>g.user_id
  AND length(trim(g.reason))>0 AND r.role IN('owner','admin'))`

const archiveCaseMemberSQL = `SELECT EXISTS(
 SELECT 1 FROM archive_investigations i
 JOIN archive_investigation_members m ON m.tenant_id=i.tenant_id AND m.investigation_id=i.id
 WHERE i.tenant_id=?1 AND i.id=?2 AND i.expires_at>?3 AND m.user_id=?4 AND m.removed_at IS NULL)`

func (q *Queries) archiveCaseGrant(ctx context.Context, tenant, user int64, scope string) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, archiveCaseGrantSQL, tenant, user, scope, timeutil.NowMicro()).Scan(&ok)
	return ok, err
}

func (q *Queries) archiveCaseMember(ctx context.Context, tenant, caseID, user int64) (bool, error) {
	granted, err := q.archiveCaseGrant(ctx, tenant, user, "full_content")
	if err != nil || !granted {
		return false, err
	}
	var member bool
	err = q.db.QueryRow(ctx, archiveCaseMemberSQL, tenant, caseID, timeutil.NowMicro(), user).Scan(&member)
	return member, err
}

// The JSON shapes below reproduce the jsonb object key order (byte length,
// then bytewise) so a SQLite-computed snapshot stays stable across reads and
// hashes identically to its stored text. Timestamps are unix microseconds
// (SQLite storage); the PG-era jsonb text form is a Phase-3 data-migration
// concern, not a Phase-1 correctness gap.
type archiveSnapshotGrant struct {
	ID        int64  `json:"id"`
	Role      string `json:"role"`
	Scope     string `json:"scope"`
	Reason    string `json:"reason"`
	Grantor   *int64 `json:"grantor"`
	ExpiresAt *int64 `json:"expires_at"`
	UpdatedAt *int64 `json:"updated_at"`
}

type archiveSnapshotMember struct {
	Grants    []archiveSnapshotGrant `json:"grants"`
	UserID    int64                  `json:"user_id"`
	RemovedAt *int64                 `json:"removed_at"`
}

type archiveSnapshotSource struct {
	ID        int64  `json:"id"`
	Size      int64  `json:"size"`
	Sha256    string `json:"sha256"`
	DeletedAt *int64 `json:"deleted_at"`
	LegalHold bool   `json:"legal_hold"`
	MailboxID *int64 `json:"mailbox_id"`
}

type archiveSnapshot struct {
	Status    string                  `json:"status"`
	CaseID    int64                   `json:"case_id"`
	Members   []archiveSnapshotMember `json:"members"`
	Sources   []archiveSnapshotSource `json:"sources"`
	Version   int64                   `json:"version"`
	ExpiresAt int64                   `json:"expires_at"`
}

// archiveCaseSnapshotText builds the urmail_archive_case_snapshot jsonb text
// for one case. Returns nil when the case exists but the actor is not a
// member (the PG function returned NULL in that case).
func (q *Queries) archiveCaseSnapshotText(ctx context.Context, tenant, caseID, user int64) ([]byte, error) {
	var snap archiveSnapshot
	err := q.db.QueryRow(ctx,
		`SELECT id, version, expires_at, status FROM archive_investigations WHERE tenant_id=?1 AND id=?2`,
		tenant, caseID).Scan(&snap.CaseID, &snap.Version, &snap.ExpiresAt, &snap.Status)
	if err != nil {
		return nil, err
	}
	member, err := q.archiveCaseMember(ctx, tenant, caseID, user)
	if err != nil {
		return nil, err
	}
	if !member {
		return nil, nil
	}
	snap.Sources = []archiveSnapshotSource{}
	rows, err := q.db.Query(ctx, `SELECT e.id, e.raw_sha256, e.size_bytes, e.mailbox_id, e.deleted_at, e.legal_hold
 FROM archive_investigation_sources s JOIN archive_entries e ON e.tenant_id=s.tenant_id AND e.id=s.archive_entry_id
 WHERE s.tenant_id=?1 AND s.investigation_id=?2 ORDER BY e.id`, tenant, caseID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var src archiveSnapshotSource
		if err := rows.Scan(&src.ID, &src.Sha256, &src.Size, &src.MailboxID, &src.DeletedAt, &src.LegalHold); err != nil {
			rows.Close()
			return nil, err
		}
		snap.Sources = append(snap.Sources, src)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	snap.Members = []archiveSnapshotMember{}
	memberRows, err := q.db.Query(ctx, `SELECT user_id, removed_at FROM archive_investigation_members
 WHERE tenant_id=?1 AND investigation_id=?2 ORDER BY user_id`, tenant, caseID)
	if err != nil {
		return nil, err
	}
	var members []archiveSnapshotMember
	for memberRows.Next() {
		var mem archiveSnapshotMember
		if err := memberRows.Scan(&mem.UserID, &mem.RemovedAt); err != nil {
			memberRows.Close()
			return nil, err
		}
		members = append(members, mem)
	}
	if err := memberRows.Err(); err != nil {
		memberRows.Close()
		return nil, err
	}
	memberRows.Close()
	for _, mem := range members {
		mem.Grants = []archiveSnapshotGrant{}
		grows, err := q.db.Query(ctx, `SELECT g.id, g.scope, g.expires_at, g.granted_by, g.reason, g.updated_at, r.role
 FROM compliance_grants g JOIN user_tenant_roles r ON r.tenant_id=g.tenant_id AND r.user_id=g.user_id
 WHERE g.tenant_id=?1 AND g.user_id=?2 AND g.scope IN('full_content','export') AND NOT g.revoked AND g.expires_at>?3
  AND g.owner_confirmed_at IS NOT NULL AND g.granted_by IS NOT NULL AND g.granted_by<>g.user_id
  AND length(trim(g.reason))>0 AND r.role IN('owner','admin') ORDER BY g.id`,
			tenant, mem.UserID, timeutil.NowMicro())
		if err != nil {
			return nil, err
		}
		for grows.Next() {
			var g archiveSnapshotGrant
			if err := grows.Scan(&g.ID, &g.Scope, &g.ExpiresAt, &g.Grantor, &g.Reason, &g.UpdatedAt, &g.Role); err != nil {
				grows.Close()
				return nil, err
			}
			mem.Grants = append(mem.Grants, g)
		}
		if err := grows.Err(); err != nil {
			grows.Close()
			return nil, err
		}
		grows.Close()
		snap.Members = append(snap.Members, mem)
	}
	return json.Marshal(snap)
}

// ArchiveCasesSnapshot returns the case snapshot jsonb text (nil when the
// actor is not a case member). Session tenant/user binding is app_pending.
func (q *Queries) ArchiveCasesSnapshot(ctx context.Context, arg ArchiveCasesSnapshotParams) ([]byte, error) {
	return q.archiveCaseSnapshotText(ctx, arg.T, arg.C, arg.U)
}

// ArchiveCasesSnapshotDigest returns the hex SHA-256 of the snapshot text,
// matching encode(sha256(convert_to(snapshot,'UTF8')),'hex').
func (q *Queries) ArchiveCasesSnapshotDigest(ctx context.Context, arg ArchiveCasesSnapshotDigestParams) (string, error) {
	snap, err := q.archiveCaseSnapshotText(ctx, arg.T, arg.C, arg.U)
	if err != nil {
		return "", err
	}
	if snap == nil {
		return "", nil
	}
	sum := sha256.Sum256(snap)
	return hex.EncodeToString(sum[:]), nil
}

// ArchiveCasesExportVisible reports whether the approved export request is
// still fully authorized for this actor (mirrors urmail_archive_export_visible).
func (q *Queries) ArchiveCasesExportVisible(ctx context.Context, arg ArchiveCasesExportVisibleParams) (bool, error) {
	var caseID, requester, approver, caseVersion, invVersion, expiresAt int64
	var state, sourceSnapshot string
	err := q.db.QueryRow(ctx, `SELECT r.investigation_id, r.state, r.expires_at, r.case_version,
 r.requester_user_id, COALESCE(r.approver_user_id,0), r.source_snapshot, i.version
 FROM archive_approval_requests r JOIN archive_investigations i ON i.tenant_id=r.tenant_id AND i.id=r.investigation_id
 WHERE r.tenant_id=?1 AND r.id=?2 AND i.status='open'`,
		arg.T, arg.A).Scan(&caseID, &state, &expiresAt, &caseVersion, &requester, &approver, &sourceSnapshot, &invVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state != "approved" || expiresAt <= timeutil.NowMicro() || approver == 0 ||
		requester == approver || caseVersion != invVersion {
		return false, nil
	}
	for _, user := range []int64{arg.U, requester, approver} {
		ok, err := q.archiveCaseMember(ctx, arg.T, caseID, user)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		granted, err := q.archiveCaseGrant(ctx, arg.T, user, "export")
		if err != nil {
			return false, err
		}
		if !granted {
			return false, nil
		}
	}
	snap, err := q.archiveCaseSnapshotText(ctx, arg.T, caseID, arg.U)
	if err != nil {
		return false, err
	}
	if snap == nil {
		return false, nil
	}
	return jsonEqual(sourceSnapshot, snap), nil
}

// jsonEqual compares two JSON documents after canonicalizing object key order
// (the PG function compared jsonb, which is order-insensitive).
func jsonEqual(a string, b []byte) bool {
	if a == string(b) {
		return true
	}
	decode := func(raw []byte) (any, bool) {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		return v, true
	}
	av, ok := decode([]byte(a))
	if !ok {
		return false
	}
	bv, ok := decode(b)
	if !ok {
		return false
	}
	am, err := json.Marshal(av)
	if err != nil {
		return false
	}
	bm, err := json.Marshal(bv)
	if err != nil {
		return false
	}
	return bytes.Equal(am, bm)
}
