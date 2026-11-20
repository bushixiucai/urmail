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
	"math"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	report "github.com/bushixiucai/urmail/internal/domain/marketingreport"
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

const archiveRetentionCandidatesWhere = ` FROM archive_entries a
 JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
 JOIN retention_policies p ON p.tenant_id=a.tenant_id
 WHERE p.active AND p.action='soft_delete' AND p.confirmed_preview IS NOT NULL
  AND (SELECT COALESCE((SELECT 1 FROM compliance_grants cg JOIN user_tenant_roles ur ON ur.tenant_id=cg.tenant_id AND ur.user_id=cg.user_id
       WHERE cg.tenant_id=p.tenant_id AND cg.user_id=p.created_by AND cg.scope='retention_manage' AND NOT cg.revoked
        AND cg.owner_confirmed_at IS NOT NULL AND ur.role IN('owner','admin')), 0))=1
  AND NOT a.legal_hold AND a.deleted_at IS NULL
  AND a.received_at < (CAST(strftime('%s','now') AS INTEGER)*1000000) - p.retention_days*86400000000
 ORDER BY a.id LIMIT 100`

const archiveRetentionCandidatesSQL = `SELECT a.id, a.tenant_id, p.confirmed_preview` + archiveRetentionCandidatesWhere

// The Mark* statements compare a two-column row value against the candidate
// set, so the key SELECT must project exactly two columns. The preview is
// supplied separately by the SET scalar subquery; projecting it here too (the
// pre-S7 shape) made the IN a 2-vs-3 arity error.
const archiveRetentionCandidateKeysSQL = `SELECT a.id, a.tenant_id` + archiveRetentionCandidatesWhere

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
WHERE (entry_id, tenant_id) IN (` + archiveRetentionCandidateKeysSQL + `)`

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
WHERE (id, tenant_id) IN (` + archiveRetentionCandidateKeysSQL + `)`

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

// ConsumeRecoveryCodeParams is the pre-B5a parameter struct for
// ConsumeRecoveryCode. It is kept verbatim (field names, json tags) because
// both the AuthStore interface and its test fake construct it.
type ConsumeRecoveryCodeParams struct {
	ID      int64  `json:"id"`
	Column2 string `json:"column_2"`
}

// ConsumeRecoveryCode is the historical name of ConsumeUserRecoveryCode. The
// PG statement rewrote the jsonb array in SQL (jsonb_agg + WITH ORDINALITY);
// the SQLite path does the same rewrite in Go, so this is a thin alias that
// keeps the handler-facing API stable.
func (q *Queries) ConsumeRecoveryCode(ctx context.Context, arg ConsumeRecoveryCodeParams) (int64, error) {
	return q.ConsumeUserRecoveryCode(ctx, arg.ID, arg.Column2)
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

// ArchiveCasesRequestApprovalParams is the parameter set for
// ArchiveCasesRequestApproval (kept with the pre-B5a name because the handler
// and its test construct it).
type ArchiveCasesRequestApprovalParams struct {
	TenantID int64  `json:"tenant_id"`
	CaseID   int64  `json:"case_id"`
	ActorID  int64  `json:"actor_id"`
	Digest   string `json:"digest"`
	Version  int64  `json:"version"`
}

// ArchiveCasesRequestApproval creates (or replays, exactly-once per digest) an
// export approval request. The PG statement used the
// urmail_archive_case_snapshot() function to freeze the case snapshot and
// least(expires_at, now()+24h) for the request deadline; both are done here,
// with the snapshot built by archiveCaseSnapshotText and the deadline clamped
// in Go. A missing case, a stale version, or a non-member actor yields
// pgx.ErrNoRows exactly like the PG INSERT ... SELECT did.
func (q *Queries) ArchiveCasesRequestApproval(ctx context.Context, arg ArchiveCasesRequestApprovalParams) (int64, error) {
	now := timeutil.NowMicro()
	var existing int64
	err := q.db.QueryRow(ctx, `SELECT r.id FROM archive_approval_requests r
WHERE r.tenant_id=?1 AND r.investigation_id=?2 AND r.requester_user_id=?3
 AND r.source_digest=?4 AND r.state='pending' AND r.expires_at>?5
ORDER BY r.id LIMIT 1`,
		arg.TenantID, arg.CaseID, arg.ActorID, arg.Digest, now).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	var caseVersion, caseExpires int64
	err = q.db.QueryRow(ctx, `SELECT version, expires_at FROM archive_investigations
WHERE tenant_id=?1 AND id=?2 AND status='open'`, arg.TenantID, arg.CaseID).Scan(&caseVersion, &caseExpires)
	if err != nil {
		return 0, err
	}
	if caseVersion != arg.Version {
		return 0, pgx.ErrNoRows
	}
	snap, err := q.archiveCaseSnapshotText(ctx, arg.TenantID, arg.CaseID, arg.ActorID)
	if err != nil {
		return 0, err
	}
	if snap == nil {
		return 0, pgx.ErrNoRows
	}
	expires := now + 24*int64(time.Hour/time.Microsecond)
	if caseExpires < expires {
		expires = caseExpires
	}
	var id int64
	err = q.db.QueryRow(ctx, `INSERT INTO archive_approval_requests(tenant_id,investigation_id,requester_user_id,action,source_digest,expires_at,case_version,source_snapshot)
SELECT ?1, i.id, ?3, 'export', ?4, ?6, i.version, ?7
FROM archive_investigations i
WHERE i.tenant_id=?1 AND i.id=?2 AND i.status='open' AND i.version=?5
 AND NOT EXISTS(SELECT 1 FROM archive_approval_requests r
  WHERE r.tenant_id=?1 AND r.investigation_id=?2 AND r.requester_user_id=?3
   AND r.source_digest=?4 AND r.state='pending' AND r.expires_at>?8)
RETURNING id`,
		arg.TenantID, arg.CaseID, arg.ActorID, arg.Digest, arg.Version, expires, snap, now).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// Lost the replay race: the concurrent request is now the pending one.
		err = q.db.QueryRow(ctx, `SELECT r.id FROM archive_approval_requests r
WHERE r.tenant_id=?1 AND r.investigation_id=?2 AND r.requester_user_id=?3
 AND r.source_digest=?4 AND r.state='pending' AND r.expires_at>?5
ORDER BY r.id LIMIT 1`,
			arg.TenantID, arg.CaseID, arg.ActorID, arg.Digest, now).Scan(&id)
	}
	return id, err
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

// ---------------------------------------------------------------------------
// S4 batch B: multi-statement procedures (archive case create, archive soft
// delete, panel credential rotation). PG raised named exceptions with SQLSTATE
// codes; the same *pgconn.PgError values are returned here so the HTTP layer's
// existing 42501→403 / 23514→409 / 40001 mapping keeps working unchanged.
// ---------------------------------------------------------------------------

// pgError reproduces a PG RAISE EXCEPTION for call sites that switch on the
// SQLSTATE code.
func pgError(code, message string) error {
	return &pgconn.PgError{Code: code, Message: message}
}

// txBeginner is the transactional seam for multi-statement procedures. pgx
// connections/pools/txs all satisfy it; the SQLite adapter (S5/B5) must
// implement Begin(context.Context) (pgx.Tx, error) so these procedures keep
// the PG functions' all-or-nothing guarantee.
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// runTx applies fn inside one transaction. A DBTX without transaction support
// is refused loudly: half-applied credential rotation or archive slices would
// be worse than a visible error.
func (q *Queries) runTx(ctx context.Context, fn func(*Queries) error) error {
	beginner, ok := q.db.(txBeginner)
	if !ok {
		return fmt.Errorf("store: procedure requires a transactional DBTX")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(q.WithTx(tx)); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// timestamptzMicros converts a pgtype.Timestamptz to SQLite unix micros.
func timestamptzMicros(ts pgtype.Timestamptz) (int64, bool) {
	if !ts.Valid {
		return 0, false
	}
	return ts.Time.UnixMicro(), true
}

var archiveCaseRequestKeyPattern = regexp.MustCompile(`^[0-9a-f-]{36}$`)

const archiveCaseExistingSQL = `SELECT id, title, purpose, expires_at FROM archive_investigations
WHERE tenant_id=?1 AND created_by=?2 AND request_key=?3`

const archiveCaseInsertSQL = `INSERT INTO archive_investigations(tenant_id,title,purpose,created_by,expires_at,request_key)
VALUES(?1,?2,?3,?4,?5,?6) RETURNING id`

const archiveCaseAddMemberSQL = `INSERT INTO archive_investigation_members(tenant_id,investigation_id,user_id)
VALUES(?1,?2,?3)`

// ArchiveCasesCreate creates (or replays, exactly-once by request_key) an
// archive investigation and enrolls its creator as a member. Mirrors
// urmail_create_archive_case: the current_setting tenant/user self-check is the
// S2 decorator's app_pending obligation; the grant + expiry bounds + request
// key idempotency are enforced here.
func (q *Queries) ArchiveCasesCreate(ctx context.Context, arg ArchiveCasesCreateParams) (int64, error) {
	expiry, ok := timestamptzMicros(arg.ExpiresAt)
	if !ok || !archiveCaseRequestKeyPattern.MatchString(arg.RequestKey) {
		return 0, pgError("42501", "CASE_NOT_AUTHORIZED")
	}
	now := timeutil.NowMicro()
	oneYear := timeutil.Micro(timeutil.FromMicro(now).AddDate(1, 0, 0))
	if expiry <= now || expiry > oneYear {
		return 0, pgError("42501", "CASE_NOT_AUTHORIZED")
	}
	granted, err := q.archiveCaseGrant(ctx, arg.TenantID, arg.ActorID, "full_content")
	if err != nil {
		return 0, err
	}
	if !granted {
		return 0, pgError("42501", "CASE_NOT_AUTHORIZED")
	}
	var id, existingExpiry int64
	var title, purpose string
	err = q.db.QueryRow(ctx, archiveCaseExistingSQL, arg.TenantID, arg.ActorID, arg.RequestKey).
		Scan(&id, &title, &purpose, &existingExpiry)
	switch {
	case err == nil:
		if title != arg.Title || purpose != arg.Purpose || existingExpiry != expiry {
			return 0, pgError("23514", "CASE_REQUEST_KEY_MISMATCH")
		}
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return 0, err
	}
	var created int64
	err = q.runTx(ctx, func(tx *Queries) error {
		if err := tx.db.QueryRow(ctx, archiveCaseInsertSQL, arg.TenantID, arg.Title, arg.Purpose,
			arg.ActorID, expiry, arg.RequestKey).Scan(&created); err != nil {
			return err
		}
		_, err := tx.db.Exec(ctx, archiveCaseAddMemberSQL, arg.TenantID, created, arg.ActorID)
		return err
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}

const softDeleteArchiveSelectSQL = `SELECT a.id FROM archive_entries a
LEFT JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE a.tenant_id=?1 AND a.id=?2 AND NOT a.legal_hold AND a.deleted_at IS NULL AND c.purge_started_at IS NULL`

const softDeleteArchiveMarkSQL = `UPDATE archive_entries SET deleted_at=?3 WHERE tenant_id=?1 AND id=?2`

const softDeleteArchivePreviewSQL = `UPDATE archive_content SET retention_preview=NULL WHERE tenant_id=?1 AND entry_id=?2`

// SoftDeleteArchiveEntry marks one archive entry deleted and drops its
// retention preview, returning pgx.ErrNoRows when the entry is legally held,
// already deleted, or mid-purge (the HTTP layer maps that to 409 LEGAL_HOLD).
// The PG function also required current_setting('urmail.user_id') to hold
// owner|admin; that session-role assertion is registered app_pending (S2
// decorator) and is not silently dropped.
func (q *Queries) SoftDeleteArchiveEntry(ctx context.Context, arg SoftDeleteArchiveEntryParams) (int64, error) {
	var id int64
	err := q.db.QueryRow(ctx, softDeleteArchiveSelectSQL, arg.TenantID, arg.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, pgx.ErrNoRows
	}
	if err != nil {
		return 0, err
	}
	now := timeutil.NowMicro()
	err = q.runTx(ctx, func(tx *Queries) error {
		if _, err := tx.db.Exec(ctx, softDeleteArchiveMarkSQL, arg.TenantID, id, now); err != nil {
			return err
		}
		_, err := tx.db.Exec(ctx, softDeleteArchivePreviewSQL, arg.TenantID, id)
		return err
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

const panelUserCredentialSQL = `SELECT 1 FROM users
WHERE id=?1 AND credential_version=?2 AND status='active'`

const panelUserPasswordSQL = `SELECT 1 FROM users
WHERE id=?1 AND password_hash=?2 AND credential_version=?3 AND status='active'`

const panelSessionSQL = `SELECT 1 FROM sessions
WHERE id=?1 AND user_id=?2 AND kind='panel' AND credential_version=?3 AND expires_at>?4`

const panelRotatePasswordSQL = `UPDATE users SET password_hash=?2, password_changed_at=?3 WHERE id=?1`

const panelSetMFASQL = `UPDATE users SET mfa_secret=?2, mfa_enabled=?3, mfa_recovery_codes=?4 WHERE id=?1`

// panelCredentialVersionSQL reads the generation after the users trigger
// panel_credential_version bumped it (SQLite RETURNING is computed before
// AFTER triggers fire, so the value must be read back).
const panelCredentialVersionSQL = `SELECT credential_version FROM users WHERE id=?1`

const panelDropOtherSessionsSQL = `DELETE FROM sessions WHERE user_id=?1 AND id<>?2`

const panelTouchKeptSessionSQL = `UPDATE sessions SET credential_version=?3 WHERE user_id=?1 AND id=?2`

const panelTouchKeptMFASessionSQL = `UPDATE sessions SET credential_version=?3, mfa_verified_at=?4
WHERE user_id=?1 AND id=?2`

const panelCredentialAuditSQL = `INSERT INTO audit_logs(actor_user_id,action,target_type,target_id,detail)
VALUES(?1,'auth.credential.rotate','users',?2,?3)`

func (q *Queries) requireCredentialRow(ctx context.Context, query string, args ...any) error {
	var one int64
	switch err := q.db.QueryRow(ctx, query, args...).Scan(&one); {
	case errors.Is(err, pgx.ErrNoRows):
		return err
	case err != nil:
		return err
	}
	return nil
}

// PlatformRotatePanelPassword rotates a panel password, revokes every other
// session and returns the new credential generation. Mirrors
// urmail_rotate_panel_password (CREDENTIAL_CHANGED→40001, SESSION_REVOKED→42501).
func (q *Queries) PlatformRotatePanelPassword(ctx context.Context, arg PlatformRotatePanelPasswordParams) (int64, error) {
	now := timeutil.NowMicro()
	err := q.requireCredentialRow(ctx, panelUserPasswordSQL, arg.UserID, arg.ExpectedHash, arg.ExpectedVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, pgError("40001", "CREDENTIAL_CHANGED")
	}
	if err != nil {
		return 0, err
	}
	err = q.requireCredentialRow(ctx, panelSessionSQL, arg.KeepSession, arg.UserID, arg.ExpectedVersion, now)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, pgError("42501", "SESSION_REVOKED")
	}
	if err != nil {
		return 0, err
	}
	var generation int64
	err = q.runTx(ctx, func(tx *Queries) error {
		if _, err := tx.db.Exec(ctx, panelRotatePasswordSQL, arg.UserID, arg.PasswordHash, now); err != nil {
			return err
		}
		if err := tx.db.QueryRow(ctx, panelCredentialVersionSQL, arg.UserID).Scan(&generation); err != nil {
			return err
		}
		if _, err := tx.db.Exec(ctx, panelDropOtherSessionsSQL, arg.UserID, arg.KeepSession); err != nil {
			return err
		}
		if _, err := tx.db.Exec(ctx, panelTouchKeptSessionSQL, arg.UserID, arg.KeepSession, generation); err != nil {
			return err
		}
		detail, err := json.Marshal(struct {
			CredentialVersion int64 `json:"credential_version"`
		}{generation})
		if err != nil {
			return err
		}
		_, err = tx.db.Exec(ctx, panelCredentialAuditSQL, arg.UserID,
			strconv.FormatInt(arg.UserID, 10), string(detail))
		return err
	})
	if err != nil {
		return 0, err
	}
	return generation, nil
}

// PlatformChangePanelMFA enables/disables panel MFA, revokes every other
// session and returns the new credential generation. Mirrors
// urmail_change_panel_mfa.
func (q *Queries) PlatformChangePanelMFA(ctx context.Context, arg PlatformChangePanelMFAParams) (int64, error) {
	now := timeutil.NowMicro()
	err := q.requireCredentialRow(ctx, panelUserCredentialSQL, arg.UserID, arg.ExpectedVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, pgError("40001", "CREDENTIAL_CHANGED")
	}
	if err != nil {
		return 0, err
	}
	err = q.requireCredentialRow(ctx, panelSessionSQL, arg.KeepSession, arg.UserID, arg.ExpectedVersion, now)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, pgError("42501", "SESSION_REVOKED")
	}
	if err != nil {
		return 0, err
	}
	var secret any
	if arg.Secret.Valid {
		secret = arg.Secret.String
	}
	enabled := int64(0)
	if arg.Enabled {
		enabled = 1
	}
	var mfaVerified any
	if arg.Enabled {
		mfaVerified = now
	}
	var generation int64
	err = q.runTx(ctx, func(tx *Queries) error {
		if _, err := tx.db.Exec(ctx, panelSetMFASQL, arg.UserID, secret, enabled, string(arg.Codes)); err != nil {
			return err
		}
		if err := tx.db.QueryRow(ctx, panelCredentialVersionSQL, arg.UserID).Scan(&generation); err != nil {
			return err
		}
		if _, err := tx.db.Exec(ctx, panelDropOtherSessionsSQL, arg.UserID, arg.KeepSession); err != nil {
			return err
		}
		if _, err := tx.db.Exec(ctx, panelTouchKeptMFASessionSQL, arg.UserID, arg.KeepSession, generation, mfaVerified); err != nil {
			return err
		}
		detail, err := json.Marshal(struct {
			CredentialVersion int64 `json:"credential_version"`
		}{generation})
		if err != nil {
			return err
		}
		_, err = tx.db.Exec(ctx, panelCredentialAuditSQL, arg.UserID,
			strconv.FormatInt(arg.UserID, 10), string(detail))
		return err
	})
	if err != nil {
		return 0, err
	}
	return generation, nil
}

// ---------------------------------------------------------------------------
// S4 batch C: DNS credential / change-plan procedures (dns_onboarding.sql).
// Faithful port of the urmail_*_dns_* plpgsql functions:
//   * pg_advisory_xact_lock(51,hashtext(domain)) -> SQLite's single-writer
//     transaction serialisation (no session semantics are dropped: the lock
//     only ever serialised writers, never carried data).
//   * urmail_dns_actor keeps its role/actor gate by reading the S2 decorator's
//     session settings (current_setting('urmail.user_id')), which is how the PG
//     function saw the caller. That seam is app_pending: the tenant self-check
//     is redundant because every caller binds tenant_id from ctx.
//   * urmail_request_onboarding loses only its subscriptions entitlement clause
//     (the subscriptions table was dropped by 0156 and is absent from the
//     SQLite schema); that gate is registered app_pending, not dropped.
// ---------------------------------------------------------------------------

const dnsActorSQL = `SELECT CAST(nullif(current_setting('urmail.user_id',true),'') AS INTEGER)
WHERE EXISTS(SELECT 1 FROM user_tenant_roles
  WHERE tenant_id=?1 AND user_id=CAST(nullif(current_setting('urmail.user_id',true),'') AS INTEGER)
   AND role IN('owner','admin'))
 AND EXISTS(SELECT 1 FROM domains
  WHERE tenant_id=?1 AND id=?2 AND status IN('active','disabled') AND verified_at IS NOT NULL AND verified_at<=?3)`

// dnsActor mirrors urmail_dns_actor: returns the acting user or the PG
// SQLSTATE 42501 DNS_AUTHORIZATION_REQUIRED.
func (q *Queries) dnsActor(ctx context.Context, tenant, domain int64) (int64, error) {
	var uid int64
	err := q.db.QueryRow(ctx, dnsActorSQL, tenant, domain, timeutil.NowMicro()).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, pgError("42501", "DNS_AUTHORIZATION_REQUIRED")
	}
	if err != nil {
		return 0, err
	}
	return uid, nil
}

// uuidText renders a pgtype.UUID the way PG's text cast does, for the TEXT
// uuid columns of the SQLite schema.
func uuidText(u pgtype.UUID) (string, bool) {
	if !u.Valid {
		return "", false
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), true
}

// dnsReasonOK mirrors length(trim(reason)) >= 10 in characters (not bytes).
func dnsReasonOK(reason string) bool {
	return utf8.RuneCountInString(strings.TrimSpace(reason)) >= 10
}

const dnsDomainStateSQL = `SELECT EXISTS(SELECT 1 FROM domains WHERE tenant_id=?1 AND id=?2 AND status=?3)`

const dnsZoneMatchSQL = `SELECT EXISTS(SELECT 1 FROM domains WHERE tenant_id=?1 AND id=?2
 AND (fqdn=?3 OR substr(fqdn,-(length(?3)+1))='.'||?3))`

const dnsUpsertCredentialSQL = `INSERT INTO dns_credentials(tenant_id,domain_id,provider,zone_name,zone_id,encrypted_secret)
VALUES(?1,?2,?3,?4,?5,?6)
ON CONFLICT(tenant_id,domain_id) DO UPDATE SET
 provider=excluded.provider,zone_name=excluded.zone_name,zone_id=excluded.zone_id,
 encrypted_secret=excluded.encrypted_secret,version=dns_credentials.version+1,
 enabled=1,revoked_at=NULL,verified_at=?7,updated_at=?7
RETURNING id`

const dnsInvalidatePlansOnCredentialSQL = `UPDATE dns_change_plans
SET status=CASE WHEN status='draft' THEN 'canceled' ELSE 'uncertain' END,
 fence=fence+1,lease_owner=NULL,lease_until=NULL,last_error_code=?3,updated_at=?4
WHERE tenant_id=?1 AND domain_id=?2 AND status IN('draft','confirmed','applying','rollback_pending')`

// SaveDNSCredential stores (or rotates) the encrypted DNS credential and
// invalidates every live plan for the domain. Mirrors
// urmail_save_dns_credential.
func (q *Queries) SaveDNSCredential(ctx context.Context, arg SaveDNSCredentialParams) (int64, error) {
	if _, err := q.dnsActor(ctx, arg.TenantID, arg.DomainID); err != nil {
		return 0, err
	}
	var active bool
	if err := q.db.QueryRow(ctx, dnsDomainStateSQL, arg.TenantID, arg.DomainID, "active").Scan(&active); err != nil {
		return 0, err
	}
	if !active {
		return 0, pgError("23514", "DNS_DOMAIN_DISABLED")
	}
	var zoneOK bool
	if err := q.db.QueryRow(ctx, dnsZoneMatchSQL, arg.TenantID, arg.DomainID, arg.ZoneName).Scan(&zoneOK); err != nil {
		return 0, err
	}
	if arg.ZoneName == "" || arg.ZoneID == "" || !strings.HasPrefix(arg.EncryptedSecret, "enc:v1:") || !zoneOK {
		return 0, pgError("23514", "DNS_ZONE_MISMATCH")
	}
	now := timeutil.NowMicro()
	var cid int64
	err := q.runTx(ctx, func(tx *Queries) error {
		if err := tx.db.QueryRow(ctx, dnsUpsertCredentialSQL, arg.TenantID, arg.DomainID, arg.Provider,
			arg.ZoneName, arg.ZoneID, arg.EncryptedSecret, now).Scan(&cid); err != nil {
			return err
		}
		_, err := tx.db.Exec(ctx, dnsInvalidatePlansOnCredentialSQL, arg.TenantID, arg.DomainID,
			"DNS_CREDENTIAL_CHANGED", now)
		return err
	})
	if err != nil {
		return 0, err
	}
	return cid, nil
}

const dnsRevokeCredentialSQL = `UPDATE dns_credentials
SET enabled=0,version=version+1,revoked_at=?3,updated_at=?3
WHERE tenant_id=?1 AND domain_id=?2`

// RevokeDNSCredential disables the domain credential and invalidates live
// plans. Mirrors urmail_revoke_dns_credential.
func (q *Queries) RevokeDNSCredential(ctx context.Context, arg RevokeDNSCredentialParams) error {
	if _, err := q.dnsActor(ctx, arg.TenantID, arg.DomainID); err != nil {
		return err
	}
	now := timeutil.NowMicro()
	return q.runTx(ctx, func(tx *Queries) error {
		if _, err := tx.db.Exec(ctx, dnsRevokeCredentialSQL, arg.TenantID, arg.DomainID, now); err != nil {
			return err
		}
		_, err := tx.db.Exec(ctx, dnsInvalidatePlansOnCredentialSQL, arg.TenantID, arg.DomainID,
			"DNS_CREDENTIAL_REVOKED", now)
		return err
	})
}

const dnsCreatePlanSQL = `INSERT INTO dns_change_plans(id,tenant_id,domain_id,credential_id,credential_version,
 kind,domain_digest,source_digest,before_records,desired_records,changes,created_by,expires_at)
VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13)`

const dnsCredentialLiveSQL = `SELECT EXISTS(SELECT 1 FROM dns_credentials
WHERE tenant_id=?1 AND domain_id=?2 AND id=?3 AND version=?4 AND enabled AND revoked_at IS NULL)`

// CreateDNSPlan records a 15-minute preview plan. Mirrors urmail_create_dns_plan.
func (q *Queries) CreateDNSPlan(ctx context.Context, arg CreateDNSPlanParams) (pgtype.UUID, error) {
	uid, err := q.dnsActor(ctx, arg.TenantID, arg.DomainID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	var active bool
	if err := q.db.QueryRow(ctx, dnsDomainStateSQL, arg.TenantID, arg.DomainID, "active").Scan(&active); err != nil {
		return pgtype.UUID{}, err
	}
	if !active {
		return pgtype.UUID{}, pgError("23514", "DNS_DOMAIN_DISABLED")
	}
	id, ok := uuidText(arg.ID)
	if !ok {
		return pgtype.UUID{}, pgError("23514", "DNS_PLAN_INVALID")
	}
	if arg.Kind == "provider" {
		if !arg.CredentialID.Valid || !arg.CredentialVersion.Valid {
			return pgtype.UUID{}, pgError("23514", "DNS_CREDENTIAL_CHANGED")
		}
		var live bool
		if err := q.db.QueryRow(ctx, dnsCredentialLiveSQL, arg.TenantID, arg.DomainID,
			arg.CredentialID.Int64, arg.CredentialVersion.Int64).Scan(&live); err != nil {
			return pgtype.UUID{}, err
		}
		if !live {
			return pgtype.UUID{}, pgError("23514", "DNS_CREDENTIAL_CHANGED")
		}
	} else if arg.CredentialID.Valid {
		return pgtype.UUID{}, pgError("23514", "DNS_PLAN_INVALID")
	}
	var credentialID, credentialVersion any
	if arg.CredentialID.Valid {
		credentialID = arg.CredentialID.Int64
	}
	if arg.CredentialVersion.Valid {
		credentialVersion = arg.CredentialVersion.Int64
	}
	_, err = q.db.Exec(ctx, dnsCreatePlanSQL, id, arg.TenantID, arg.DomainID, credentialID,
		credentialVersion, arg.Kind, arg.DomainDigest, arg.SourceDigest,
		string(arg.BeforeRecords), string(arg.DesiredRecords), string(arg.Changes), uid,
		timeutil.NowMicro()+15*60*1000000)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return arg.ID, nil
}

const dnsPlanForUpdateSQL = `SELECT status,kind,domain_id,credential_id,credential_version,source_digest,
 changes,expires_at FROM dns_change_plans WHERE tenant_id=?1 AND id=?2`

type dnsPlanRow struct {
	Status            string
	Kind              string
	DomainID          int64
	CredentialID      *int64
	CredentialVersion *int64
	SourceDigest      string
	Changes           string
	ExpiresAt         int64
}

// dnsPlan loads the plan inside the caller's transaction (SQLite serialises
// writers, so the PG FOR UPDATE is unnecessary).
func (q *Queries) dnsPlan(ctx context.Context, tenant int64, id string) (dnsPlanRow, error) {
	var p dnsPlanRow
	err := q.db.QueryRow(ctx, dnsPlanForUpdateSQL, tenant, id).Scan(&p.Status, &p.Kind, &p.DomainID,
		&p.CredentialID, &p.CredentialVersion, &p.SourceDigest, &p.Changes, &p.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, pgError("23514", "DNS_PLAN_NOT_FOUND")
	}
	return p, err
}

// dnsConflictFree reports whether the preview's "conflicts" array is empty,
// mirroring `jsonb_array_length(p.changes->'conflicts')>0` in
// urmail_confirm_dns_plan: a missing key is NULL (not >0), while an explicit
// null or a non-array makes PG raise, so confirm must be refused there too.
func dnsConflictFree(changes string) bool {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(changes), &doc); err != nil {
		return false
	}
	raw, ok := doc["conflicts"]
	if !ok {
		return true
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	list, ok := value.([]any)
	if !ok {
		return false
	}
	return len(list) == 0
}

const dnsConfirmPlanSQL = `UPDATE dns_change_plans
SET status='confirmed',confirmed_by=?3,confirmed_at=?4,updated_at=?4
WHERE tenant_id=?1 AND id=?2`

// ConfirmDNSPlan validates the preview digest, expiry and credential version,
// then confirms the plan. Mirrors urmail_confirm_dns_plan.
func (q *Queries) ConfirmDNSPlan(ctx context.Context, arg ConfirmDNSPlanParams) error {
	id, ok := uuidText(arg.ID)
	if !ok {
		return pgError("23514", "DNS_PLAN_NOT_FOUND")
	}
	p, err := q.dnsPlan(ctx, arg.TenantID, id)
	if err != nil {
		return err
	}
	uid, err := q.dnsActor(ctx, arg.TenantID, p.DomainID)
	if err != nil {
		return err
	}
	var active bool
	if err := q.db.QueryRow(ctx, dnsDomainStateSQL, arg.TenantID, p.DomainID, "active").Scan(&active); err != nil {
		return err
	}
	if !active {
		return pgError("23514", "DNS_DOMAIN_DISABLED")
	}
	now := timeutil.NowMicro()
	if p.Status != "draft" || p.ExpiresAt <= now || p.SourceDigest != arg.SourceDigest ||
		!dnsConflictFree(p.Changes) {
		return pgError("23514", "DNS_PLAN_CONFIRMATION_INVALID")
	}
	if p.Kind == "provider" {
		if p.CredentialID == nil || p.CredentialVersion == nil {
			return pgError("23514", "DNS_CREDENTIAL_CHANGED")
		}
		var live bool
		if err := q.db.QueryRow(ctx, dnsCredentialLiveSQL, arg.TenantID, p.DomainID,
			*p.CredentialID, *p.CredentialVersion).Scan(&live); err != nil {
			return err
		}
		if !live {
			return pgError("23514", "DNS_CREDENTIAL_CHANGED")
		}
	}
	_, err = q.db.Exec(ctx, dnsConfirmPlanSQL, arg.TenantID, id, uid, now)
	return err
}

const dnsCancelPlanSQL = `UPDATE dns_change_plans
SET status='canceled',fence=fence+1,lease_owner=NULL,lease_until=NULL,
 last_error_code='CANCELED_BY_TENANT',updated_at=?3
WHERE tenant_id=?1 AND id=?2`

// CancelDNSPlan cancels a draft or confirmed plan. Mirrors
// urmail_cancel_dns_plan.
func (q *Queries) CancelDNSPlan(ctx context.Context, arg CancelDNSPlanParams) error {
	id, ok := uuidText(arg.ID)
	if !ok {
		return pgError("23514", "DNS_PLAN_NOT_FOUND")
	}
	p, err := q.dnsPlan(ctx, arg.TenantID, id)
	if err != nil {
		return err
	}
	if _, err := q.dnsActor(ctx, arg.TenantID, p.DomainID); err != nil {
		return err
	}
	if (p.Status != "draft" && p.Status != "confirmed") || !dnsReasonOK(arg.Reason) {
		return pgError("23514", "DNS_PLAN_CANCEL_INVALID")
	}
	_, err = q.db.Exec(ctx, dnsCancelPlanSQL, arg.TenantID, id, timeutil.NowMicro())
	return err
}

const onboardingRestoreUnreconciledSQL = `SELECT EXISTS(
 SELECT 1 FROM onboarding_tasks t
 WHERE t.tenant_id=?1 AND t.domain_id=?2 AND t.status='blocked'
  AND (COALESCE(t.last_error_code,'')||COALESCE(t.last_error,'')) LIKE '%RESTORE_RECONCILIATION_REQUIRED%')
AND NOT EXISTS(
 SELECT 1 FROM dns_change_plans p
 WHERE p.tenant_id=?1 AND p.domain_id=?2 AND p.status='completed'
  AND p.updated_at>(SELECT max(t.updated_at) FROM onboarding_tasks t
   WHERE t.tenant_id=?1 AND t.domain_id=?2 AND t.status='blocked'))`

const onboardingSupersedeSQL = `UPDATE onboarding_tasks
SET status='failed',fence=fence+1,leased_until=NULL,leased_by=NULL,
 last_error_code='SUPERSEDED_BY_EXPLICIT_RETRY',updated_at=?3
WHERE tenant_id=?1 AND domain_id=?2 AND status IN('pending','running','failed','blocked')`

const onboardingInsertTaskSQL = `INSERT INTO onboarding_tasks(tenant_id,domain_id,status,step,max_attempts)
VALUES(?1,?2,'pending','bootstrap_config',12)`

const onboardingResetDomainSQL = `UPDATE domains SET provisioning_state='pending' WHERE tenant_id=?1 AND id=?2`

// RequestOnboardingRetry supersedes every live task with a fresh bootstrap
// task. Mirrors urmail_request_onboarding minus its subscriptions
// entitlement clause (table dropped by 0156; registered app_pending).
func (q *Queries) RequestOnboardingRetry(ctx context.Context, arg RequestOnboardingRetryParams) error {
	if _, err := q.dnsActor(ctx, arg.TenantID, arg.DomainID); err != nil {
		return err
	}
	var active bool
	if err := q.db.QueryRow(ctx, dnsDomainStateSQL, arg.TenantID, arg.DomainID, "active").Scan(&active); err != nil {
		return err
	}
	if !active || !dnsReasonOK(arg.Reason) {
		return pgError("23514", "ONBOARDING_RETRY_INVALID")
	}
	var unreconciled bool
	if err := q.db.QueryRow(ctx, onboardingRestoreUnreconciledSQL, arg.TenantID, arg.DomainID).Scan(&unreconciled); err != nil {
		return err
	}
	if unreconciled {
		return pgError("23514", "RESTORE_DNS_RECONCILIATION_REQUIRED")
	}
	now := timeutil.NowMicro()
	return q.runTx(ctx, func(tx *Queries) error {
		if _, err := tx.db.Exec(ctx, onboardingSupersedeSQL, arg.TenantID, arg.DomainID, now); err != nil {
			return err
		}
		if _, err := tx.db.Exec(ctx, onboardingInsertTaskSQL, arg.TenantID, arg.DomainID); err != nil {
			return err
		}
		_, err := tx.db.Exec(ctx, onboardingResetDomainSQL, arg.TenantID, arg.DomainID)
		return err
	})
}

const dnsRollbackPlanSQL = `UPDATE dns_change_plans
SET status='rollback_pending',fence=fence+1,lease_owner=NULL,lease_until=NULL,
 last_error_code='ROLLBACK_EXPLICITLY_REQUESTED',updated_at=?3
WHERE tenant_id=?1 AND id=?2`

// RequestDNSRollback requests a reviewed rollback of a completed plan. Mirrors
// urmail_rollback_dns_plan.
func (q *Queries) RequestDNSRollback(ctx context.Context, arg RequestDNSRollbackParams) error {
	id, ok := uuidText(arg.ID)
	if !ok {
		return pgError("23514", "DNS_PLAN_NOT_FOUND")
	}
	p, err := q.dnsPlan(ctx, arg.TenantID, id)
	if err != nil {
		return err
	}
	if _, err := q.dnsActor(ctx, arg.TenantID, p.DomainID); err != nil {
		return err
	}
	if p.Status != "completed" || !dnsReasonOK(arg.Reason) || p.Kind == "manual" {
		return pgError("23514", "DNS_ROLLBACK_REQUIRES_REVIEW")
	}
	now := timeutil.NowMicro()
	return q.runTx(ctx, func(tx *Queries) error {
		if _, err := tx.db.Exec(ctx, onboardingResetDomainSQL, arg.TenantID, p.DomainID); err != nil {
			return err
		}
		_, err := tx.db.Exec(ctx, dnsRollbackPlanSQL, arg.TenantID, id, now)
		return err
	})
}

const dnsCompletePlanSQL = `UPDATE dns_change_plans
SET status='completed',fence=fence+1,lease_owner=NULL,lease_until=NULL,
 last_error_code='READ_ONLY_OBSERVATION_CONFIRMED',updated_at=?3
WHERE tenant_id=?1 AND id=?2`

const dnsRecordActionsObservedSQL = `UPDATE dns_record_actions
SET status='applied',owned=0,last_error_code='MANUAL_OBSERVATION_UNOWNED',updated_at=?3
WHERE tenant_id=?1 AND plan_id=?2 AND status IN('pending','submitted','uncertain')`

// ReviewDNSPlan resolves an uncertain plan as a read-only observation and
// leaves every unowned record unowned. Mirrors urmail_review_dns_plan.
func (q *Queries) ReviewDNSPlan(ctx context.Context, arg ReviewDNSPlanParams) error {
	id, ok := uuidText(arg.ID)
	if !ok {
		return pgError("23514", "DNS_PLAN_NOT_FOUND")
	}
	p, err := q.dnsPlan(ctx, arg.TenantID, id)
	if err != nil {
		return err
	}
	if _, err := q.dnsActor(ctx, arg.TenantID, p.DomainID); err != nil {
		return err
	}
	if p.Status != "uncertain" || !dnsReasonOK(arg.Reason) {
		return pgError("23514", "DNS_REVIEW_REQUIRED")
	}
	now := timeutil.NowMicro()
	return q.runTx(ctx, func(tx *Queries) error {
		if _, err := tx.db.Exec(ctx, dnsCompletePlanSQL, arg.TenantID, id, now); err != nil {
			return err
		}
		_, err := tx.db.Exec(ctx, dnsRecordActionsObservedSQL, arg.TenantID, id, now)
		return err
	})
}

const dnsAbandonPlanSQL = `UPDATE dns_change_plans
SET status='failed',fence=fence+1,lease_owner=NULL,lease_until=NULL,
 last_error_code='ABANDONED_WITH_UNRESOLVED_EXTERNAL_FACTS',updated_at=?3
WHERE tenant_id=?1 AND id=?2`

// AbandonDNSPlan fails an uncertain/failed plan without claiming anything
// about external state. Mirrors urmail_abandon_dns_plan.
func (q *Queries) AbandonDNSPlan(ctx context.Context, arg AbandonDNSPlanParams) error {
	id, ok := uuidText(arg.ID)
	if !ok {
		return pgError("23514", "DNS_PLAN_NOT_FOUND")
	}
	p, err := q.dnsPlan(ctx, arg.TenantID, id)
	if err != nil {
		return err
	}
	if _, err := q.dnsActor(ctx, arg.TenantID, p.DomainID); err != nil {
		return err
	}
	if (p.Status != "uncertain" && p.Status != "failed") || !dnsReasonOK(arg.Reason) {
		return pgError("23514", "DNS_REVIEW_REQUIRED")
	}
	_, err = q.db.Exec(ctx, dnsAbandonPlanSQL, arg.TenantID, id, timeutil.NowMicro())
	return err
}

// ---------------------------------------------------------------------------
// S4 batch D: marketing dispatch + flow procedures.
//
// Every urmail_*_marketing* function below ran inside the caller's transaction
// and asserted the caller's identity through session settings:
// current_setting('urmail.tenant_id'/'urmail.user_id') plus an owner|admin
// membership row, or current_setting('role')='urmail_platform' for the
// dispatch worker. Those settings are the S2 decorator's / S5 adapter's
// contract, so the Go ports read them through current_setting() exactly like
// the PG functions did: an unbound session therefore fails closed instead of
// granting access. The role/tenant assertions that cannot be reproduced from
// data are the same app_pending seam already recorded for the DNS and archive
// procedures, never silently dropped.
//
// pg_advisory_xact_lock(tenant) calls are replaced by SQLite's single-writer
// transaction serialisation; the lock only ever ordered writers and carried no
// data.
// ---------------------------------------------------------------------------

const sessionActorSQL = `SELECT CAST(nullif(current_setting('urmail.tenant_id',true),'') AS INTEGER),
 CAST(nullif(current_setting('urmail.user_id',true),'') AS INTEGER),
 COALESCE(current_setting('role',true),'')`

// sessionActor reads the tenant/user/role the PG procedures asserted through
// current_setting(). Every field is 0/"" when the setting is unbound, which
// makes the owner|admin lookup and the worker gate fail closed.
func (q *Queries) sessionActor(ctx context.Context) (tenant, user int64, role string, err error) {
	var tid, uid *int64
	if err := q.db.QueryRow(ctx, sessionActorSQL).Scan(&tid, &uid, &role); err != nil {
		return 0, 0, "", err
	}
	if tid != nil {
		tenant = *tid
	}
	if uid != nil {
		user = *uid
	}
	return tenant, user, role, nil
}

const tenantAdminSQL = `SELECT EXISTS(SELECT 1 FROM user_tenant_roles
 WHERE tenant_id=?1 AND user_id=CAST(nullif(current_setting('urmail.user_id',true),'') AS INTEGER)
  AND role IN('owner','admin'))`

// tenantAdmin mirrors the `user_id=current_setting(...) AND role IN
// ('owner','admin')` predicate shared by the tenant-facing marketing and AI
// configuration procedures.
func (q *Queries) tenantAdmin(ctx context.Context, tenant int64) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, tenantAdminSQL, tenant).Scan(&ok)
	return ok, err
}

const marketingWorkerSQL = `SELECT COALESCE(current_setting('role',true),'')='urmail_platform'`

// requireMarketingWorker reproduces urmail_platform's worker-only gate. A
// session without the role setting fails closed with the PG SQLSTATE 42501 the
// callers already map.
func (q *Queries) requireMarketingWorker(ctx context.Context) error {
	var ok bool
	if err := q.db.QueryRow(ctx, marketingWorkerSQL).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return pgError("42501", "MARKETING_WORKER_REQUIRED")
	}
	return nil
}

const marketingMailAllowedSQL = `SELECT EXISTS(
 SELECT 1 FROM tenants t WHERE t.id=?1 AND t.status='active'
  AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=t.id)
   OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=t.id
        AND gd.status='active' AND gd.provisioning_state='ready')))`

// mailAllowed is urmail_mail_allowed after 0156 (subscriptions dropped).
func (q *Queries) mailAllowed(ctx context.Context, tenant int64) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, marketingMailAllowedSQL, tenant).Scan(&ok)
	return ok, err
}

const marketingLimitsSQL = `SELECT
 (SELECT COALESCE(
   (SELECT CAST(json_extract(value,'$') AS TEXT) FROM settings
     WHERE scope='tenant' AND scope_id=?1 AND key='marketing_daily_recipient_limit'),
   (SELECT CAST(json_extract(value,'$') AS TEXT) FROM settings
     WHERE scope='platform' AND scope_id=0 AND key='marketing_daily_recipient_limit'))),
 (SELECT COALESCE(
   (SELECT CAST(json_extract(value,'$') AS TEXT) FROM settings
     WHERE scope='tenant' AND scope_id=?1 AND key='marketing_rate_per_minute'),
   (SELECT CAST(json_extract(value,'$') AS TEXT) FROM settings
     WHERE scope='platform' AND scope_id=0 AND key='marketing_rate_per_minute')))`

var (
	marketingDayLimitPattern  = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	marketingRateLimitPattern = regexp.MustCompile(`^[1-9][0-9]{0,5}$`)
)

// marketingLimits is urmail_marketing_limits: tenant setting, else platform
// setting, and both limits are unusable unless each matches PG's regex.
func (q *Queries) marketingLimits(ctx context.Context, tenant int64) (day, rate int64, ok bool, err error) {
	var dayText, rateText *string
	if err := q.db.QueryRow(ctx, marketingLimitsSQL, tenant).Scan(&dayText, &rateText); err != nil {
		return 0, 0, false, err
	}
	if dayText == nil || rateText == nil ||
		!marketingDayLimitPattern.MatchString(*dayText) ||
		!marketingRateLimitPattern.MatchString(*rateText) {
		return 0, 0, false, nil
	}
	day, _ = strconv.ParseInt(*dayText, 10, 64)
	rate, _ = strconv.ParseInt(*rateText, 10, 64)
	return day, rate, true, nil
}

const marketingTimezoneSQL = `SELECT COALESCE(
 (SELECT CAST(json_extract(value,'$') AS TEXT) FROM settings
   WHERE scope='tenant' AND scope_id=?1 AND key='business_timezone'),
 (SELECT CAST(json_extract(s.value,'$') AS TEXT) FROM settings s
   JOIN tenants t ON t.brand_id=s.scope_id
   WHERE t.id=?1 AND s.scope='brand' AND s.key='business_timezone'),
 (SELECT CAST(json_extract(value,'$') AS TEXT) FROM settings
   WHERE scope='platform' AND scope_id=0 AND key='business_timezone'),
 'Asia/Shanghai')`

// marketingTimezone is urmail_business_timezone. PG validates against
// pg_timezone_names; Go validates with time.LoadLocation and rejects "Local"
// exactly like the handler-side aiQuotaLocation, keeping INVALID_TIMEZONE.
func (q *Queries) marketingTimezone(ctx context.Context, tenant int64) (*time.Location, error) {
	var tz string
	if err := q.db.QueryRow(ctx, marketingTimezoneSQL, tenant).Scan(&tz); err != nil {
		return nil, err
	}
	if tz == "" || tz == "Local" {
		return nil, pgError("23514", "INVALID_TIMEZONE")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, pgError("23514", "INVALID_TIMEZONE")
	}
	return loc, nil
}

// marketingNextBoundary is PG's
// (date_trunc('day', t AT TIME ZONE tz) + interval '1 day') AT TIME ZONE tz:
// the next local midnight in loc.
func marketingNextBoundary(micros int64, loc *time.Location) int64 {
	t := time.UnixMicro(micros).In(loc)
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc).UnixMicro()
}

const marketingChannelSQL = `SELECT EXISTS(SELECT 1 FROM marketing_channels
 WHERE tenant_id=?1 AND id=?2 AND enabled AND kind='dedicated_gateway')`

const marketingSenderSQL = `SELECT EXISTS(SELECT 1 FROM marketing_channels ch
 JOIN mailboxes m ON m.tenant_id=ch.tenant_id AND m.address=ch.sender_address
 JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
 WHERE ch.tenant_id=?1 AND ch.id=?2 AND m.status='active' AND m.send_enabled
  AND d.status='active' AND d.provisioning_state='ready')`

// marketingSenderReadySQL is the flow variant, which additionally calls
// urmail_domain_mail_ready (verified_at IS NOT NULL).
const marketingSenderReadySQL = `SELECT EXISTS(SELECT 1 FROM marketing_channels ch
 JOIN mailboxes m ON m.tenant_id=ch.tenant_id AND m.address=ch.sender_address
 JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
 WHERE ch.tenant_id=?1 AND ch.id=?2 AND m.status='active' AND m.send_enabled
  AND d.status='active' AND d.verified_at IS NOT NULL AND d.provisioning_state='ready')`

type marketingWindowRow struct {
	ID         int64
	EndsAt     int64
	Timezone   string
	UsedCount  int64
	NextSendAt int64
}

const marketingLatestWindowSQL = `SELECT id,ends_at,timezone,used_count,next_send_at
 FROM marketing_quota_windows WHERE tenant_id=?1 ORDER BY starts_at DESC LIMIT 1`

const marketingInsertWindowSQL = `INSERT INTO marketing_quota_windows(tenant_id,starts_at,ends_at,timezone)
 VALUES(?1,?2,?3,?4) RETURNING id`

const marketingConsumeWindowSQL = `UPDATE marketing_quota_windows
 SET used_count=used_count+1,next_send_at=?3 WHERE tenant_id=?1 AND id=?2`

// marketingReserveSend is the rate/quota window shared by
// urmail_authorize_marketing_send and urmail_begin_marketing_flow_send: it
// opens a new window when the last one closed or the tenant timezone changed,
// then reserves one slot. ok=false means the caller must return status
// verbatim (quota_exhausted / rate_limited).
func (q *Queries) marketingReserveSend(ctx context.Context, tenant int64, now, dayLimit, rateLimit int64,
	loc *time.Location) (status string, windowID int64, ok bool, err error) {
	var w marketingWindowRow
	err = q.db.QueryRow(ctx, marketingLatestWindowSQL, tenant).
		Scan(&w.ID, &w.EndsAt, &w.Timezone, &w.UsedCount, &w.NextSendAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = nil
		w.ID = 0
	case err != nil:
		return "", 0, false, err
	}
	if w.ID == 0 || w.EndsAt <= now {
		start := now
		boundary := marketingNextBoundary(now, loc)
		// A timezone change on a short DST day must not shrink the window
		// below 24h: PG pushes the boundary one more local midnight out.
		if w.ID != 0 && w.Timezone != loc.String() && boundary < start+86400000000 {
			boundary = marketingNextBoundary(start+86400000000, loc)
		}
		if err := q.db.QueryRow(ctx, marketingInsertWindowSQL, tenant, start, boundary, loc.String()).
			Scan(&w.ID); err != nil {
			return "", 0, false, err
		}
		w.UsedCount = 0
		w.NextSendAt = math.MinInt64
	}
	if w.UsedCount >= dayLimit {
		return "quota_exhausted", 0, false, nil
	}
	if w.NextSendAt > now {
		return "rate_limited", 0, false, nil
	}
	next := now + int64(math.Round(60000000.0/float64(rateLimit)))
	if _, err := q.db.Exec(ctx, marketingConsumeWindowSQL, tenant, w.ID, next); err != nil {
		return "", 0, false, err
	}
	return "", w.ID, true, nil
}

const marketingCampaignForScheduleSQL = `SELECT status,channel_id,template_id,list_id
 FROM marketing_campaigns WHERE tenant_id=?1 AND id=?2`

const marketingCampaignSnapshotSQL = `UPDATE marketing_campaigns
SET subject_snapshot=t.subject,body_snapshot=t.text_body,scheduled_at=?3,status='scheduled'
FROM marketing_templates t
WHERE marketing_campaigns.tenant_id=?1 AND marketing_campaigns.id=?2
 AND t.tenant_id=?1 AND t.id=marketing_campaigns.template_id`

const marketingEnqueueRecipientsSQL = `INSERT INTO marketing_recipients(tenant_id,campaign_id,email)
SELECT ?1,?2,mc.email FROM marketing_contacts mc
WHERE mc.tenant_id=?1 AND mc.list_id=?3 AND mc.opted_in
 AND NOT EXISTS(SELECT 1 FROM marketing_suppressions s WHERE s.tenant_id=?1 AND s.email=mc.email)
ON CONFLICT(campaign_id,email) DO NOTHING`

// ScheduleMarketingCampaign mirrors urmail_schedule_marketing: it snapshots the
// template, schedules the campaign and enqueues every opted-in, non-suppressed
// list contact. Every precondition returns a status string instead of an error,
// as the PG function did.
func (q *Queries) ScheduleMarketingCampaign(ctx context.Context, arg ScheduleMarketingCampaignParams) (string, error) {
	var result string
	err := q.runTx(ctx, func(tx *Queries) error {
		var err error
		result, err = tx.scheduleMarketing(ctx, arg)
		return err
	})
	return result, err
}

func (q *Queries) scheduleMarketing(ctx context.Context, arg ScheduleMarketingCampaignParams) (string, error) {
	ok, err := q.tenantAdmin(ctx, arg.TenantID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", pgError("42501", "MARKETING_FORBIDDEN")
	}
	var status string
	var channelID, templateID, listID int64
	err = q.db.QueryRow(ctx, marketingCampaignForScheduleSQL, arg.TenantID, arg.CampaignID).
		Scan(&status, &channelID, &templateID, &listID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "not_found", nil
	}
	if err != nil {
		return "", err
	}
	if status != "draft" {
		return status, nil
	}
	at, valid := timestamptzMicros(arg.ScheduledAt)
	if !valid {
		return "invalid_schedule", nil
	}
	allowed, err := q.mailAllowed(ctx, arg.TenantID)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "entitlement_denied", nil
	}
	if _, _, configured, err := q.marketingLimits(ctx, arg.TenantID); err != nil {
		return "", err
	} else if !configured {
		return "quota_unconfigured", nil
	}
	var channelOK bool
	if err := q.db.QueryRow(ctx, marketingChannelSQL, arg.TenantID, channelID).Scan(&channelOK); err != nil {
		return "", err
	}
	if !channelOK {
		return "channel_unavailable", nil
	}
	var senderOK bool
	if err := q.db.QueryRow(ctx, marketingSenderSQL, arg.TenantID, channelID).Scan(&senderOK); err != nil {
		return "", err
	}
	if !senderOK {
		return "sender_unavailable", nil
	}
	if _, err := q.db.Exec(ctx, marketingCampaignSnapshotSQL, arg.TenantID, arg.CampaignID, at); err != nil {
		return "", err
	}
	if _, err := q.db.Exec(ctx, marketingEnqueueRecipientsSQL, arg.TenantID, arg.CampaignID, listID); err != nil {
		return "", err
	}
	return "scheduled", nil
}

type marketingFlowPositionRow struct {
	ID                int64
	LeaseOwner        *string
	LeaseFence        int64
	LeaseUntil        *int64
	DeliveryUncertain bool
	FlowReserved      bool
	FlowWindowID      *int64
	StoppedAt         *int64
	Unsubscribed      bool
	StepKey           string
	WaitUntil         *int64
	EntryID           *int64
}

const marketingFlowPositionSQL = `SELECT id,lease_owner,lease_fence,lease_until,delivery_uncertain,
 flow_reserved,flow_window_id,stopped_at,unsubscribed,step_key,wait_until,entry_id
 FROM marketing_flow_positions WHERE tenant_id=?1 AND flow_id=?2 AND email=?3`

func (q *Queries) marketingFlowPosition(ctx context.Context, tenant, flow int64, email string) (marketingFlowPositionRow, error) {
	var r marketingFlowPositionRow
	err := q.db.QueryRow(ctx, marketingFlowPositionSQL, tenant, flow, email).Scan(
		&r.ID, &r.LeaseOwner, &r.LeaseFence, &r.LeaseUntil, &r.DeliveryUncertain,
		&r.FlowReserved, &r.FlowWindowID, &r.StoppedAt, &r.Unsubscribed, &r.StepKey,
		&r.WaitUntil, &r.EntryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, pgx.ErrNoRows
	}
	return r, err
}

// leaseStale mirrors PG's
// `lease_owner IS DISTINCT FROM p_owner OR lease_fence<>p_fence
//
//	OR lease_until IS NULL OR lease_until<=now()`.
func leaseStale(r marketingFlowPositionRow, owner string, fence, now int64, checkExpiry bool) bool {
	if r.LeaseOwner == nil || *r.LeaseOwner != owner || r.LeaseFence != fence {
		return true
	}
	if checkExpiry && (r.LeaseUntil == nil || *r.LeaseUntil <= now) {
		return true
	}
	return false
}

const marketingQuotaReleaseSQL = `UPDATE marketing_quota_windows
 SET used_count=max(0,used_count-1) WHERE tenant_id=?1 AND id=?2`

const marketingFlowRetrySQL = `UPDATE marketing_flow_positions
SET delivery_uncertain=0,flow_reserved=0,flow_window_id=NULL,last_error='',
 next_attempt_at=?3,lease_owner=NULL,lease_until=NULL,lease_fence=lease_fence+1,updated_at=?3
WHERE tenant_id=?1 AND id=?2`

const marketingFlowRetrySelectSQL = `SELECT p.id,p.stopped_at,p.unsubscribed,p.lease_until,
 p.delivery_uncertain,p.last_error,p.flow_reserved,p.flow_window_id
FROM marketing_flow_positions p
JOIN marketing_flow_versions v ON v.tenant_id=p.tenant_id AND v.runtime_flow_id=p.flow_id
JOIN marketing_flows f ON f.tenant_id=p.tenant_id AND f.id=p.flow_id
WHERE p.tenant_id=?1 AND p.id=?2 AND v.definition_id=?3 AND f.status='active'`

// RetryMarketingFlowInstance mirrors urmail_retry_marketing_flow_instance: only
// a failed or delivery-uncertain position whose lease lapsed is made runnable
// again, and any reserved quota slot is given back.
func (q *Queries) RetryMarketingFlowInstance(ctx context.Context, arg RetryMarketingFlowInstanceParams) (bool, error) {
	_, _, role, err := q.sessionActor(ctx)
	if err != nil {
		return false, err
	}
	if role != "urmail_platform" {
		ok, err := q.tenantAdmin(ctx, arg.PTenant)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, pgError("42501", "MARKETING_ADMIN_REQUIRED")
		}
	}
	var retried bool
	err = q.runTx(ctx, func(tx *Queries) error {
		var err error
		retried, err = tx.retryMarketingFlowInstance(ctx, arg)
		return err
	})
	return retried, err
}

func (q *Queries) retryMarketingFlowInstance(ctx context.Context, arg RetryMarketingFlowInstanceParams) (bool, error) {
	var stoppedAt, leaseUntil *int64
	var unsubscribed, uncertain, reserved bool
	var lastError string
	var windowID *int64
	var positionID int64
	err := q.db.QueryRow(ctx, marketingFlowRetrySelectSQL, arg.PTenant, arg.PPosition, arg.PDefinition).
		Scan(&positionID, &stoppedAt, &unsubscribed, &leaseUntil, &uncertain, &lastError, &reserved, &windowID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := timeutil.NowMicro()
	if stoppedAt != nil || unsubscribed ||
		(leaseUntil != nil && *leaseUntil > now) ||
		(!uncertain && lastError == "") ||
		(uncertain && !arg.PConfirmedNotAccepted) {
		return false, nil
	}
	if reserved && windowID != nil {
		if _, err := q.db.Exec(ctx, marketingQuotaReleaseSQL, arg.PTenant, *windowID); err != nil {
			return false, err
		}
	}
	if _, err := q.db.Exec(ctx, marketingFlowRetrySQL, arg.PTenant, positionID, now); err != nil {
		return false, err
	}
	return true, nil
}

var marketingFlowAuditActions = map[string]bool{
	"published": true, "deactivated": true, "enrolled": true,
	"retry_requested": true, "instance_stopped": true,
}

const marketingFlowDefinitionSQL = `SELECT EXISTS(SELECT 1 FROM marketing_flow_definitions
 WHERE tenant_id=?1 AND id=?2)`

const marketingFlowAuditInsertSQL = `INSERT INTO audit_logs(tenant_id,actor_user_id,action,target_type,target_id,detail)
VALUES(?1,?2,?3,'marketing_flow',?4,?5)`

// RecordMarketingFlowAudit mirrors urmail_record_marketing_flow_audit. The audit
// table's append-only INSERT trigger still rejects this row until S6 removes the
// no_insert guard, exactly as it did for RecordTenantAudit in batch A.
func (q *Queries) RecordMarketingFlowAudit(ctx context.Context, arg RecordMarketingFlowAuditParams) error {
	_, actor, _, err := q.sessionActor(ctx)
	if err != nil {
		return err
	}
	if actor == 0 {
		return pgError("42501", "MARKETING_ADMIN_REQUIRED")
	}
	ok, err := q.tenantAdmin(ctx, arg.PTenant)
	if err != nil {
		return err
	}
	if !ok {
		return pgError("42501", "MARKETING_ADMIN_REQUIRED")
	}
	var definitionExists bool
	if err := q.db.QueryRow(ctx, marketingFlowDefinitionSQL, arg.PTenant, arg.PDefinition).
		Scan(&definitionExists); err != nil {
		return err
	}
	if !definitionExists {
		return pgError("42501", "MARKETING_ADMIN_REQUIRED")
	}
	if !marketingFlowAuditActions[arg.PAction] {
		return pgError("22023", "INVALID_FLOW_AUDIT")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(arg.PDetail, &doc); err != nil || doc == nil {
		return pgError("22023", "INVALID_FLOW_AUDIT")
	}
	// PG measures octet_length(p_detail::text). jsonb renders ", " / ": " so its
	// text can be a few bytes longer than the compact form; the compact bound is
	// the stricter side and never admits a payload PG would reject.
	var compact bytes.Buffer
	if err := json.Compact(&compact, arg.PDetail); err != nil || compact.Len() > 4000 {
		return pgError("22023", "INVALID_FLOW_AUDIT")
	}
	_, err = q.db.Exec(ctx, marketingFlowAuditInsertSQL, arg.PTenant, actor,
		"marketing.flow."+arg.PAction, strconv.FormatInt(arg.PDefinition, 10),
		string(compact.Bytes()))
	return err
}

const marketingRecipientSQL = `SELECT status,lease_owner,lease_until,fence,email,campaign_id,
 window_id,reserved,attempts FROM marketing_recipients WHERE tenant_id=?1 AND id=?2`

type marketingRecipientRow struct {
	Status     string
	LeaseOwner *string
	LeaseUntil *int64
	Fence      int64
	Email      string
	CampaignID int64
	WindowID   *int64
	Reserved   bool
	Attempts   int64
}

func (q *Queries) marketingRecipient(ctx context.Context, tenant, id int64) (marketingRecipientRow, error) {
	var r marketingRecipientRow
	err := q.db.QueryRow(ctx, marketingRecipientSQL, tenant, id).Scan(&r.Status, &r.LeaseOwner,
		&r.LeaseUntil, &r.Fence, &r.Email, &r.CampaignID, &r.WindowID, &r.Reserved, &r.Attempts)
	return r, err
}

const marketingCampaignRunSQL = `SELECT status,channel_id,list_id,scheduled_at
 FROM marketing_campaigns WHERE tenant_id=?1 AND id=?2`

const marketingRecipientSuppressSQL = `UPDATE marketing_recipients
 SET status='suppressed',completed_at=?3,result_code='suppressed' WHERE tenant_id=?1 AND id=?2`

const marketingRecipientSubmitSQL = `UPDATE marketing_recipients
 SET status='submitting',window_id=?3,reserved=1,attempts=attempts+1 WHERE tenant_id=?1 AND id=?2`

const marketingCampaignRunningSQL = `UPDATE marketing_campaigns SET status='running'
 WHERE tenant_id=?1 AND id=?2`

const marketingSuppressionHitSQL = `SELECT EXISTS(SELECT 1 FROM marketing_suppressions
 WHERE tenant_id=?1 AND email=?2)
 OR NOT EXISTS(SELECT 1 FROM marketing_contacts
  WHERE tenant_id=?1 AND list_id=?3 AND email=?2 AND opted_in)`

// PlatformAuthorizeMarketingSend mirrors urmail_authorize_marketing_send. It
// reserves a quota slot and moves the recipient to submitting, or returns the
// terminal status that the worker logs and skips on.
func (q *Queries) PlatformAuthorizeMarketingSend(ctx context.Context, arg PlatformAuthorizeMarketingSendParams) (string, error) {
	var result string
	err := q.runTx(ctx, func(tx *Queries) error {
		var err error
		result, err = tx.authorizeMarketingSend(ctx, arg)
		return err
	})
	return result, err
}

func (q *Queries) authorizeMarketingSend(ctx context.Context, arg PlatformAuthorizeMarketingSendParams) (string, error) {
	if err := q.requireMarketingWorker(ctx); err != nil {
		return "", err
	}
	now := timeutil.NowMicro()
	r, err := q.marketingRecipient(ctx, arg.TenantID, arg.RecipientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "stale", nil
	}
	if err != nil {
		return "", err
	}
	if r.Status != "leased" || r.LeaseOwner == nil || *r.LeaseOwner != arg.LeaseOwner ||
		r.Fence != arg.Fence || r.LeaseUntil == nil || *r.LeaseUntil <= now {
		return "stale", nil
	}
	var campaignStatus string
	var channelID, listID, scheduledAt int64
	err = q.db.QueryRow(ctx, marketingCampaignRunSQL, arg.TenantID, r.CampaignID).
		Scan(&campaignStatus, &channelID, &listID, &scheduledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// The recipient FK guarantees a campaign row; treat an orphan as paused.
		return "paused", nil
	}
	if err != nil {
		return "", err
	}
	if (campaignStatus != "scheduled" && campaignStatus != "running") || scheduledAt > now {
		return "paused", nil
	}
	var suppressed bool
	if err := q.db.QueryRow(ctx, marketingSuppressionHitSQL, arg.TenantID, r.Email, listID).
		Scan(&suppressed); err != nil {
		return "", err
	}
	if suppressed {
		if _, err := q.db.Exec(ctx, marketingRecipientSuppressSQL, arg.TenantID, arg.RecipientID, now); err != nil {
			return "", err
		}
		return "suppressed", nil
	}
	allowed, err := q.mailAllowed(ctx, arg.TenantID)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "entitlement_denied", nil
	}
	dayLimit, rateLimit, configured, err := q.marketingLimits(ctx, arg.TenantID)
	if err != nil {
		return "", err
	}
	if !configured {
		return "quota_unconfigured", nil
	}
	var channelOK bool
	if err := q.db.QueryRow(ctx, marketingChannelSQL, arg.TenantID, channelID).Scan(&channelOK); err != nil {
		return "", err
	}
	if !channelOK {
		return "channel_unavailable", nil
	}
	var senderOK bool
	if err := q.db.QueryRow(ctx, marketingSenderSQL, arg.TenantID, channelID).Scan(&senderOK); err != nil {
		return "", err
	}
	if !senderOK {
		return "sender_unavailable", nil
	}
	loc, err := q.marketingTimezone(ctx, arg.TenantID)
	if err != nil {
		return "", err
	}
	status, windowID, reserved, err := q.marketingReserveSend(ctx, arg.TenantID, now, dayLimit, rateLimit, loc)
	if err != nil {
		return "", err
	}
	if !reserved {
		return status, nil
	}
	if _, err := q.db.Exec(ctx, marketingRecipientSubmitSQL, arg.TenantID, arg.RecipientID, windowID); err != nil {
		return "", err
	}
	if _, err := q.db.Exec(ctx, marketingCampaignRunningSQL, arg.TenantID, r.CampaignID); err != nil {
		return "", err
	}
	return "authorized", nil
}

const marketingRecipientFinishSQL = `UPDATE marketing_recipients
SET status=CASE WHEN ?3='failed' AND attempts<5 THEN 'queued' ELSE ?3 END,
 reserved=CASE WHEN ?3 IN('failed','bounced') THEN 0 ELSE 1 END,
 result_code=?3,
 completed_at=CASE WHEN ?3='failed' AND attempts<5 THEN NULL ELSE ?4 END,
 next_attempt_at=?5,lease_until=NULL
WHERE tenant_id=?1 AND id=?2`

const marketingSuppressionBounceSQL = `INSERT INTO marketing_suppressions(tenant_id,email,reason)
 VALUES(?1,?2,'bounce') ON CONFLICT(tenant_id,email) DO NOTHING`

var marketingFinishResults = map[string]bool{
	"sent": true, "uncertain": true, "failed": true, "bounced": true,
}

// PlatformFinishMarketingSend mirrors urmail_finish_marketing_send: it records
// the SMTP outcome, refunds a bounced/failed slot, retries a failed send up to
// five times with exponential backoff, and suppresses bounce addresses.
func (q *Queries) PlatformFinishMarketingSend(ctx context.Context, arg PlatformFinishMarketingSendParams) (bool, error) {
	if !marketingFinishResults[arg.Result] {
		return false, pgError("42501", "MARKETING_WORKER_REQUIRED")
	}
	if err := q.requireMarketingWorker(ctx); err != nil {
		return false, err
	}
	var finished bool
	err := q.runTx(ctx, func(tx *Queries) error {
		var err error
		finished, err = tx.finishMarketingSend(ctx, arg)
		return err
	})
	return finished, err
}

func (q *Queries) finishMarketingSend(ctx context.Context, arg PlatformFinishMarketingSendParams) (bool, error) {
	now := timeutil.NowMicro()
	r, err := q.marketingRecipient(ctx, arg.TenantID, arg.RecipientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if r.Status != "submitting" || r.LeaseOwner == nil || *r.LeaseOwner != arg.LeaseOwner ||
		r.Fence != arg.Fence || r.LeaseUntil == nil || *r.LeaseUntil <= now {
		return false, nil
	}
	if (arg.Result == "failed" || arg.Result == "bounced") && r.Reserved && r.WindowID != nil {
		if _, err := q.db.Exec(ctx, marketingQuotaReleaseSQL, arg.TenantID, *r.WindowID); err != nil {
			return false, err
		}
	}
	backoff := int64(1) << uint(min(r.Attempts, 10))
	if _, err := q.db.Exec(ctx, marketingRecipientFinishSQL, arg.TenantID, arg.RecipientID,
		arg.Result, now, now+backoff*1000000); err != nil {
		return false, err
	}
	if arg.Result == "bounced" {
		if _, err := q.db.Exec(ctx, marketingSuppressionBounceSQL, arg.TenantID, r.Email); err != nil {
			return false, err
		}
	}
	return true, nil
}

const marketingRecipientUncertainSQL = `SELECT status,reserved,window_id,campaign_id
 FROM marketing_recipients WHERE tenant_id=?1 AND id=?2`

const marketingCampaignStatusSQL = `SELECT status FROM marketing_campaigns WHERE tenant_id=?1 AND id=?2`

const marketingAttemptResolveSQL = `UPDATE marketing_recipients
SET status=CASE WHEN ?3='accepted' THEN 'sent' WHEN ?4='canceled' THEN 'canceled' ELSE 'queued' END,
 reserved=CASE WHEN ?3='accepted' THEN 1 ELSE 0 END,
 fence=fence+1,lease_owner=NULL,lease_until=NULL,resolution_reason=?5,
 result_code='manually_confirmed_'||?3,next_attempt_at=?6
WHERE tenant_id=?1 AND id=?2`

// PlatformResolveMarketingAttempt mirrors urmail_resolve_marketing_attempt: an
// operator says whether an uncertain submit actually reached the provider.
func (q *Queries) PlatformResolveMarketingAttempt(ctx context.Context, arg PlatformResolveMarketingAttemptParams) (bool, error) {
	if (arg.Outcome != "accepted" && arg.Outcome != "not_submitted") ||
		utf8.RuneCountInString(strings.TrimSpace(arg.Reason)) < 1 ||
		utf8.RuneCountInString(strings.TrimSpace(arg.Reason)) > 2048 {
		return false, pgError("42501", "MARKETING_RESOLUTION_INVALID")
	}
	if err := q.requireMarketingWorker(ctx); err != nil {
		return false, err
	}
	var resolved bool
	err := q.runTx(ctx, func(tx *Queries) error {
		var err error
		resolved, err = tx.resolveMarketingAttempt(ctx, arg)
		return err
	})
	return resolved, err
}

func (q *Queries) resolveMarketingAttempt(ctx context.Context, arg PlatformResolveMarketingAttemptParams) (bool, error) {
	var status string
	var reserved bool
	var windowID *int64
	var campaignID int64
	err := q.db.QueryRow(ctx, marketingRecipientUncertainSQL, arg.TenantID, arg.RecipientID).
		Scan(&status, &reserved, &windowID, &campaignID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status != "uncertain" {
		return false, nil
	}
	var campaignStatus string
	if err := q.db.QueryRow(ctx, marketingCampaignStatusSQL, arg.TenantID, campaignID).
		Scan(&campaignStatus); err != nil {
		return false, err
	}
	if arg.Outcome == "not_submitted" && reserved && windowID != nil {
		if _, err := q.db.Exec(ctx, marketingQuotaReleaseSQL, arg.TenantID, *windowID); err != nil {
			return false, err
		}
	}
	if _, err := q.db.Exec(ctx, marketingAttemptResolveSQL, arg.TenantID, arg.RecipientID,
		arg.Outcome, campaignStatus, arg.Reason, timeutil.NowMicro()); err != nil {
		return false, err
	}
	return true, nil
}

const marketingFeedbackChannelSQL = `SELECT tenant_id FROM marketing_channels WHERE id=?1`

const marketingFeedbackRecipientSQL = `SELECT r.status,r.email FROM marketing_recipients r
WHERE r.tenant_id=?1 AND r.id=?2 AND EXISTS(SELECT 1 FROM marketing_campaigns c
 WHERE c.tenant_id=?1 AND c.id=r.campaign_id AND c.channel_id=?3)`

const marketingFeedbackPriorSQL = `SELECT payload_hash FROM marketing_feedback
 WHERE channel_id=?1 AND event_id=?2`

const marketingFeedbackInsertSQL = `INSERT INTO marketing_feedback(channel_id,event_id,tenant_id,recipient_id,status,payload_hash)
 VALUES(?1,?2,?3,?4,?5,?6)`

const marketingSuppressionUpsertSQL = `INSERT INTO marketing_suppressions(tenant_id,email,reason)
 VALUES(?1,?2,?3)
ON CONFLICT(tenant_id,email) DO UPDATE SET reason=CASE
 WHEN marketing_suppressions.reason IN('unsubscribe','bounce','complaint')
 THEN marketing_suppressions.reason ELSE excluded.reason END`

const marketingRecipientBouncedSQL = `UPDATE marketing_recipients
SET status='bounced',result_code=?3,completed_at=?4,fence=fence+1 WHERE tenant_id=?1 AND id=?2`

var (
	marketingFeedbackStatuses = map[string]bool{"bounced": true, "complaint": true, "deferred": true}
	marketingPayloadHashShape = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// PlatformRecordMarketingFeedback mirrors urmail_record_marketing_feedback: an
// idempotent provider report keyed on (channel, event) that suppresses hard
// bounces and complaints.
func (q *Queries) PlatformRecordMarketingFeedback(ctx context.Context, arg PlatformRecordMarketingFeedbackParams) (string, error) {
	eventLen := utf8.RuneCountInString(arg.EventID)
	if !marketingFeedbackStatuses[arg.Status] || eventLen < 1 || eventLen > 200 ||
		!marketingPayloadHashShape.MatchString(arg.PayloadHash) {
		return "", pgError("42501", "MARKETING_FEEDBACK_INVALID")
	}
	if err := q.requireMarketingWorker(ctx); err != nil {
		return "", err
	}
	var result string
	err := q.runTx(ctx, func(tx *Queries) error {
		var err error
		result, err = tx.recordMarketingFeedback(ctx, arg)
		return err
	})
	return result, err
}

func (q *Queries) recordMarketingFeedback(ctx context.Context, arg PlatformRecordMarketingFeedbackParams) (string, error) {
	var tenant *int64
	err := q.db.QueryRow(ctx, marketingFeedbackChannelSQL, arg.ChannelID).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return "not_found", nil
	}
	if err != nil {
		return "", err
	}
	if tenant == nil {
		return "not_found", nil
	}
	var status, email string
	err = q.db.QueryRow(ctx, marketingFeedbackRecipientSQL, *tenant, arg.RecipientID, arg.ChannelID).
		Scan(&status, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "not_found", nil
	}
	if err != nil {
		return "", err
	}
	switch status {
	case "submitting", "sent", "uncertain", "bounced":
	default:
		return "not_found", nil
	}
	var prior *string
	err = q.db.QueryRow(ctx, marketingFeedbackPriorSQL, arg.ChannelID, arg.EventID).Scan(&prior)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if prior != nil {
		if *prior == arg.PayloadHash {
			return "duplicate", nil
		}
		return "conflict", nil
	}
	if _, err := q.db.Exec(ctx, marketingFeedbackInsertSQL, arg.ChannelID, arg.EventID, *tenant,
		arg.RecipientID, arg.Status, arg.PayloadHash); err != nil {
		return "", err
	}
	if arg.Status == "bounced" || arg.Status == "complaint" {
		reason := "bounce"
		if arg.Status == "complaint" {
			reason = "complaint"
		}
		if _, err := q.db.Exec(ctx, marketingSuppressionUpsertSQL, *tenant, email, reason); err != nil {
			return "", err
		}
		if _, err := q.db.Exec(ctx, marketingRecipientBouncedSQL, *tenant, arg.RecipientID,
			arg.Status, timeutil.NowMicro()); err != nil {
			return "", err
		}
	}
	return "recorded", nil
}

const marketingRecipientRevokedSQL = `UPDATE marketing_recipients
SET status='suppressed',reserved=0,result_code='unsubscribed_mid_run',completed_at=?3,
 lease_owner=NULL,lease_until=NULL WHERE tenant_id=?1 AND id=?2`

// PlatformMarkMarketingRecipientRevoked mirrors
// urmail_mark_marketing_recipient_revoked: a mid-run unsubscribe freezes the
// reserved slot and cancels the send (note: PG checks the lease owner and fence
// but not lease expiry here).
func (q *Queries) PlatformMarkMarketingRecipientRevoked(ctx context.Context, arg PlatformMarkMarketingRecipientRevokedParams) (bool, error) {
	if err := q.requireMarketingWorker(ctx); err != nil {
		return false, err
	}
	var revoked bool
	err := q.runTx(ctx, func(tx *Queries) error {
		var err error
		revoked, err = tx.markMarketingRecipientRevoked(ctx, arg)
		return err
	})
	return revoked, err
}

func (q *Queries) markMarketingRecipientRevoked(ctx context.Context, arg PlatformMarkMarketingRecipientRevokedParams) (bool, error) {
	r, err := q.marketingRecipient(ctx, arg.TenantID, arg.RecipientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if r.Status != "submitting" || r.LeaseOwner == nil || *r.LeaseOwner != arg.LeaseOwner || r.Fence != arg.Fence {
		return false, nil
	}
	if r.Reserved && r.WindowID != nil {
		if _, err := q.db.Exec(ctx, marketingQuotaReleaseSQL, arg.TenantID, *r.WindowID); err != nil {
			return false, err
		}
	}
	if _, err := q.db.Exec(ctx, marketingRecipientRevokedSQL, arg.TenantID, arg.RecipientID,
		timeutil.NowMicro()); err != nil {
		return false, err
	}
	return true, nil
}

const marketingRecipientSkippedSQL = `UPDATE marketing_recipients
SET status='canceled',reserved=0,result_code=?3,completed_at=?4,lease_owner=NULL,lease_until=NULL
WHERE tenant_id=?1 AND id=?2`

var marketingSkipReasons = map[string]bool{
	"entitlement_denied": true, "quota_unconfigured": true,
	"channel_unavailable": true, "sender_unavailable": true, "timezone_invalid": true,
}

// PlatformSkipMarketingRecipient mirrors urmail_skip_marketing_recipient: it
// parks a leased recipient as canceled with the blocking reason.
func (q *Queries) PlatformSkipMarketingRecipient(ctx context.Context, arg PlatformSkipMarketingRecipientParams) (bool, error) {
	if !marketingSkipReasons[arg.Reason] {
		return false, pgError("42501", "MARKETING_WORKER_REQUIRED")
	}
	if err := q.requireMarketingWorker(ctx); err != nil {
		return false, err
	}
	var skipped bool
	err := q.runTx(ctx, func(tx *Queries) error {
		var err error
		skipped, err = tx.skipMarketingRecipient(ctx, arg)
		return err
	})
	return skipped, err
}

func (q *Queries) skipMarketingRecipient(ctx context.Context, arg PlatformSkipMarketingRecipientParams) (bool, error) {
	r, err := q.marketingRecipient(ctx, arg.TenantID, arg.RecipientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if r.Status != "leased" || r.LeaseOwner == nil || *r.LeaseOwner != arg.LeaseOwner || r.Fence != arg.Fence {
		return false, nil
	}
	if r.Reserved && r.WindowID != nil {
		if _, err := q.db.Exec(ctx, marketingQuotaReleaseSQL, arg.TenantID, *r.WindowID); err != nil {
			return false, err
		}
	}
	now := timeutil.NowMicro()
	if _, err := q.db.Exec(ctx, marketingRecipientSkippedSQL, arg.TenantID, arg.RecipientID,
		arg.Reason, now); err != nil {
		return false, err
	}
	return true, nil
}

const marketingFlowClearReservationSQL = `UPDATE marketing_flow_positions
SET delivery_uncertain=0,flow_reserved=0,flow_window_id=NULL
WHERE tenant_id=?1 AND flow_id=?2 AND email=?3`

const marketingFlowActiveSQL = `SELECT EXISTS(SELECT 1 FROM marketing_flows
 WHERE tenant_id=?1 AND id=?2 AND status='active')`

const marketingFlowStepExistsSQL = `SELECT EXISTS(SELECT 1 FROM marketing_flow_steps
 WHERE tenant_id=?1 AND flow_id=?2 AND step_key=?3)`

const marketingFlowStepUpdateSQL = `UPDATE marketing_flow_positions
SET step_key=?4,unsubscribed=?5,wait_until=NULL,updated_at=?6
WHERE tenant_id=?1 AND flow_id=?2 AND email=?3`

const marketingFlowStepReadSQL = `SELECT step_key,unsubscribed,wait_until,delivery_uncertain
 FROM marketing_flow_positions WHERE tenant_id=?1 AND flow_id=?2 AND email=?3`

const marketingFlowAdvanceSQL = `SELECT EXISTS(SELECT 1 FROM tenants t
 WHERE t.id=?1 AND t.status='active'
  AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=t.id)
   OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=t.id
        AND gd.status='active' AND gd.provisioning_state='ready')))
 AND EXISTS(SELECT 1 FROM marketing_flow_positions p
  WHERE p.tenant_id=?1 AND p.flow_id=?2 AND p.email=?3 AND NOT p.unsubscribed)
 AND NOT EXISTS(SELECT 1 FROM marketing_suppressions s
  WHERE s.tenant_id=?1 AND s.email=?3)`

// commitMarketingFlowStep is urmail_commit_marketing_flow_step: the
// linearization point that re-checks uncertainty, unsubscribe, the wait and the
// sending gate before moving a position.
func (q *Queries) commitMarketingFlowStep(ctx context.Context, tenant, flow int64, email, event, next string) (string, error) {
	var stepKey string
	var unsubscribed, uncertain bool
	var waitUntil *int64
	err := q.db.QueryRow(ctx, marketingFlowStepReadSQL, tenant, flow, email).
		Scan(&stepKey, &unsubscribed, &waitUntil, &uncertain)
	if errors.Is(err, pgx.ErrNoRows) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if uncertain {
		return "uncertain", nil
	}
	now := timeutil.NowMicro()
	blockedWait := waitUntil != nil && *waitUntil > now
	advance := false
	if !unsubscribed && !blockedWait {
		if err := q.db.QueryRow(ctx, marketingFlowAdvanceSQL, tenant, flow, email).Scan(&advance); err != nil {
			return "", err
		}
	}
	if unsubscribed || blockedWait || !advance {
		return "blocked", nil
	}
	if event == "unsubscribed" {
		unsubscribed = true
	}
	if next != "" {
		stepKey = next
	}
	if _, err := q.db.Exec(ctx, marketingFlowStepUpdateSQL, tenant, flow, email, stepKey, unsubscribed, now); err != nil {
		return "", err
	}
	return stepKey, nil
}

// PlatformCommitMarketingFlowFenced mirrors
// urmail_commit_marketing_flow_fenced: a fenced commit that clears the
// uncertainty/reservation marker when the worker confirmed the delivery, then
// delegates to the step commit.
func (q *Queries) PlatformCommitMarketingFlowFenced(ctx context.Context, arg PlatformCommitMarketingFlowFencedParams) (string, error) {
	var result string
	err := q.runTx(ctx, func(tx *Queries) error {
		var err error
		result, err = tx.commitMarketingFlowFenced(ctx, arg)
		return err
	})
	return result, err
}

func (q *Queries) commitMarketingFlowFenced(ctx context.Context, arg PlatformCommitMarketingFlowFencedParams) (string, error) {
	r, err := q.marketingFlowPosition(ctx, arg.PTenant, arg.PFlow, arg.PEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	now := timeutil.NowMicro()
	if arg.POwner == "" || leaseStale(r, arg.POwner, arg.PFence, now, true) {
		return "stale_lease", nil
	}
	if arg.PConfirmed {
		if _, err := q.db.Exec(ctx, marketingFlowClearReservationSQL, arg.PTenant, arg.PFlow, arg.PEmail); err != nil {
			return "", err
		}
	}
	if r.StoppedAt != nil {
		return "blocked", nil
	}
	var active bool
	if err := q.db.QueryRow(ctx, marketingFlowActiveSQL, arg.PTenant, arg.PFlow).Scan(&active); err != nil {
		return "", err
	}
	if !active {
		return "blocked", nil
	}
	if arg.PNext != "" {
		var exists bool
		if err := q.db.QueryRow(ctx, marketingFlowStepExistsSQL, arg.PTenant, arg.PFlow, arg.PNext).
			Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return "blocked", nil
		}
	}
	if r.DeliveryUncertain && !arg.PConfirmed {
		return "uncertain", nil
	}
	event := ""
	if arg.PUnsubscribed {
		event = "unsubscribed"
	}
	return q.commitMarketingFlowStep(ctx, arg.PTenant, arg.PFlow, arg.PEmail, event, arg.PNext)
}

// PlatformCommitMarketingFlowWait mirrors the same PG function used for a
// wait/condition step: no send happens, but the step commits atomically.
func (q *Queries) PlatformCommitMarketingFlowWait(ctx context.Context, arg PlatformCommitMarketingFlowWaitParams) (string, error) {
	var result string
	err := q.runTx(ctx, func(tx *Queries) error {
		var err error
		result, err = tx.commitMarketingFlowStep(ctx, arg.PTenant, arg.PFlow, arg.PEmail, arg.Event, arg.NextKey)
		return err
	})
	return result, err
}

const marketingFlowEntrySuppressedSQL = `SELECT EXISTS(SELECT 1 FROM marketing_suppressions
 WHERE tenant_id=?1 AND email=?2)
 OR NOT EXISTS(SELECT 1 FROM marketing_flow_entries e
  JOIN marketing_contacts c ON c.tenant_id=e.tenant_id AND c.list_id=e.list_id AND c.email=?2
  WHERE e.tenant_id=?1 AND e.id=?3 AND c.opted_in)`

const marketingFlowReserveSQL = `UPDATE marketing_flow_positions
SET delivery_uncertain=1,flow_reserved=1,flow_window_id=?4,updated_at=?5
WHERE tenant_id=?1 AND flow_id=?2 AND email=?3`

// PlatformBeginMarketingFlowSend mirrors urmail_begin_marketing_flow_send: it
// reserves a quota slot and marks the position uncertain until the worker
// commits the outcome.
func (q *Queries) PlatformBeginMarketingFlowSend(ctx context.Context, arg PlatformBeginMarketingFlowSendParams) (string, error) {
	if err := q.requireMarketingWorker(ctx); err != nil {
		return "", err
	}
	var result string
	err := q.runTx(ctx, func(tx *Queries) error {
		var err error
		result, err = tx.beginMarketingFlowSend(ctx, arg)
		return err
	})
	return result, err
}

func (q *Queries) beginMarketingFlowSend(ctx context.Context, arg PlatformBeginMarketingFlowSendParams) (string, error) {
	now := timeutil.NowMicro()
	r, err := q.marketingFlowPosition(ctx, arg.PTenant, arg.PFlow, arg.PEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return "stale_lease", nil
	}
	if err != nil {
		return "", err
	}
	if leaseStale(r, arg.POwner, arg.PFence, now, true) {
		return "stale_lease", nil
	}
	if r.DeliveryUncertain || r.FlowReserved {
		return "uncertain", nil
	}
	if r.StoppedAt != nil || r.Unsubscribed {
		return "paused", nil
	}
	var active bool
	if err := q.db.QueryRow(ctx, marketingFlowActiveSQL, arg.PTenant, arg.PFlow).Scan(&active); err != nil {
		return "", err
	}
	if !active {
		return "paused", nil
	}
	var suppressed bool
	if err := q.db.QueryRow(ctx, marketingFlowEntrySuppressedSQL, arg.PTenant, arg.PEmail, r.EntryID).
		Scan(&suppressed); err != nil {
		return "", err
	}
	if suppressed {
		return "suppressed", nil
	}
	allowed, err := q.mailAllowed(ctx, arg.PTenant)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "entitlement_denied", nil
	}
	dayLimit, rateLimit, configured, err := q.marketingLimits(ctx, arg.PTenant)
	if err != nil {
		return "", err
	}
	if !configured {
		return "quota_unconfigured", nil
	}
	var channelOK bool
	if err := q.db.QueryRow(ctx, marketingChannelSQL, arg.PTenant, arg.PChannel).Scan(&channelOK); err != nil {
		return "", err
	}
	if !channelOK {
		return "channel_unavailable", nil
	}
	var senderOK bool
	if err := q.db.QueryRow(ctx, marketingSenderReadySQL, arg.PTenant, arg.PChannel).Scan(&senderOK); err != nil {
		return "", err
	}
	if !senderOK {
		return "sender_unavailable", nil
	}
	loc, err := q.marketingTimezone(ctx, arg.PTenant)
	if err != nil {
		return "", err
	}
	status, windowID, reserved, err := q.marketingReserveSend(ctx, arg.PTenant, now, dayLimit, rateLimit, loc)
	if err != nil {
		return "", err
	}
	if !reserved {
		return status, nil
	}
	if _, err := q.db.Exec(ctx, marketingFlowReserveSQL, arg.PTenant, arg.PFlow, arg.PEmail,
		windowID, now); err != nil {
		return "", err
	}
	return "authorized", nil
}

// PlatformReleaseMarketingFlowReservation mirrors
// urmail_release_marketing_flow_reservation: it refunds the reservation and
// clears the flags, and deliberately leaves the lease untouched like the PG
// function does.
func (q *Queries) PlatformReleaseMarketingFlowReservation(ctx context.Context, arg PlatformReleaseMarketingFlowReservationParams) (bool, error) {
	var released bool
	err := q.runTx(ctx, func(tx *Queries) error {
		var err error
		released, err = tx.releaseMarketingFlowReservation(ctx, arg)
		return err
	})
	return released, err
}

func (q *Queries) releaseMarketingFlowReservation(ctx context.Context, arg PlatformReleaseMarketingFlowReservationParams) (bool, error) {
	r, err := q.marketingFlowPosition(ctx, arg.PTenant, arg.PFlow, arg.PEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := timeutil.NowMicro()
	if leaseStale(r, arg.POwner, arg.PFence, now, true) {
		return false, nil
	}
	if r.FlowReserved && r.FlowWindowID != nil {
		if _, err := q.db.Exec(ctx, marketingQuotaReleaseSQL, arg.PTenant, *r.FlowWindowID); err != nil {
			return false, err
		}
	}
	if _, err := q.db.Exec(ctx, marketingFlowClearReservationSQL, arg.PTenant, arg.PFlow, arg.PEmail); err != nil {
		return false, err
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// S4 batch E: AI configuration procedures (the last db-function-call residue).
//
// urmail_configure_tenant_ai / urmail_consent_tenant_ai /
// urmail_configure_tenant_ai_governance all asserted `current_setting(
// 'urmail.tenant_id') IS NOT NULL AND current_setting('urmail.user_id') IS NOT
// NULL` plus an owner|admin membership row, so the ports read the same settings
// and fail closed on an unbound session. The parts of those procedures that
// depended on session state alone — AI_TENANT_CONSENT_REQUIRED /
// AI_QUOTA_PLATFORM_REQUIRED in the ai_configuration_consent_guard and
// nonplatform_ai_quota_guard triggers — stay app_pending (S2 decorator / S5
// adapter), exactly as recorded in trigger-port.json.
//
// configuration_digest is a sha256 the PG guard trigger computes from the
// configuration row; that computation is app_pending too, so the SQLite column
// keeps its '' default and urmail_consent_tenant_ai's digest comparison behaves
// like the PG function body: an allow request whose digest does not match the
// stored value is refused rather than silently consented.
// ---------------------------------------------------------------------------

const aiConfigExistsSQL = `SELECT EXISTS(SELECT 1 FROM ai_provider_configs WHERE tenant_id=?1)`

const aiConfigureSQL = `INSERT INTO ai_provider_configs(
 tenant_id,mode,provider,endpoint,model,encrypted_key,enabled,local_only,
 enterprise_mode,enterprise_provider,enterprise_endpoint,enterprise_model,
 enterprise_encrypted_key,enterprise_enabled,enterprise_local_only,active_service,updated_at
)
VALUES(?1,?2,?3,?4,?5,?6,?7,?8,
 ?2,?3,?4,?5,?6,?7,?8,'enterprise',(CAST(strftime('%s','now') AS INTEGER)*1000000))
ON CONFLICT(tenant_id) DO UPDATE SET
 enterprise_mode=excluded.enterprise_mode,
 enterprise_provider=excluded.enterprise_provider,
 enterprise_endpoint=excluded.enterprise_endpoint,
 enterprise_model=excluded.enterprise_model,
 enterprise_encrypted_key=CASE WHEN excluded.encrypted_key<>'' THEN excluded.encrypted_key ELSE ai_provider_configs.enterprise_encrypted_key END,
 enterprise_enabled=excluded.enterprise_enabled,
 enterprise_local_only=excluded.enterprise_local_only,
 active_service='enterprise',
 mode=excluded.mode,
 provider=excluded.provider,
 endpoint=excluded.endpoint,
 model=excluded.model,
 encrypted_key=CASE WHEN excluded.encrypted_key<>'' THEN excluded.encrypted_key ELSE ai_provider_configs.enterprise_encrypted_key END,
 enabled=excluded.enabled,
 local_only=excluded.local_only,
 updated_at=excluded.updated_at`

// PlatformConfigureTenantAI mirrors urmail_configure_tenant_ai: it writes the
// tenant's own (enterprise) provider *and* the enterprise snapshot, keeping the
// previous enterprise key when the caller sent an empty key. PG deliberately
// reads ai_provider_configs.enterprise_encrypted_key for both columns, so a
// first write without a key keeps ” rather than resurrecting a platform key.
func (q *Queries) PlatformConfigureTenantAI(ctx context.Context, arg PlatformConfigureTenantAIParams) error {
	if arg.Mode != "byo" && arg.Mode != "local" {
		return pgError("23514", "AI_CONFIGURATION_INVALID")
	}
	if (arg.Mode == "local") != (arg.Provider == "ollama") {
		return pgError("23514", "AI_CONFIGURATION_INVALID")
	}
	if arg.LocalOnly && arg.Provider != "ollama" {
		return pgError("23514", "AI_CONFIGURATION_INVALID")
	}
	return q.runTx(ctx, func(tx *Queries) error {
		tenant, actor, _, err := tx.sessionActor(ctx)
		if err != nil {
			return err
		}
		if tenant == 0 || actor == 0 {
			return pgError("42501", "AI_CONFIGURATION_FORBIDDEN")
		}
		ok, err := tx.tenantAdmin(ctx, tenant)
		if err != nil {
			return err
		}
		if !ok {
			return pgError("42501", "AI_CONFIGURATION_FORBIDDEN")
		}
		var existed bool
		if err := tx.db.QueryRow(ctx, aiConfigExistsSQL, tenant).Scan(&existed); err != nil {
			return err
		}
		if _, err = tx.db.Exec(ctx, aiConfigureSQL, tenant, arg.Mode, arg.Provider, arg.Endpoint,
			arg.Model, arg.EncryptedKey, arg.Enabled, arg.LocalOnly); err != nil {
			return err
		}
		// PG's guard trigger revokes the stored cloud consent when an app-role
		// *update* switches the tenant to local mode (TG_OP='UPDATE'), so a stale
		// consent cannot outlive the export policy. SQLite has no session role to
		// test (app_pending), so the port applies the revoke on every local-mode
		// update: that is the strict direction and never grants anything.
		if existed && arg.Mode == "local" {
			if _, err := tx.db.Exec(ctx, aiConsentResetSQL, tenant, actor); err != nil {
				return err
			}
		}
		_, err = tx.refreshAIConfigurationDigest(ctx, tenant)
		return err
	})
}

const aiConsentConfigSQL = `SELECT provider,enabled,configuration_digest,cloud_scope_hash
 FROM ai_provider_configs WHERE tenant_id=?1`

const aiConsentUpdateSQL = `UPDATE ai_provider_configs SET
 tenant_local_only=?2,cloud_consent_digest=?3,cloud_consent_scope_hash=?4,
 cloud_consent_by=?5,cloud_consent_at=?6,cloud_consent_revoked_at=?7
WHERE tenant_id=?1`

// TenantConsentAICloud mirrors urmail_consent_tenant_ai: granting cloud consent
// must cite the digest and scope hash of the *current* enabled non-ollama
// configuration, while revoking always succeeds and records the timestamp.
func (q *Queries) TenantConsentAICloud(ctx context.Context, arg TenantConsentAICloudParams) error {
	return q.runTx(ctx, func(tx *Queries) error {
		tenant, actor, _, err := tx.sessionActor(ctx)
		if err != nil {
			return err
		}
		if tenant == 0 || actor == 0 {
			return pgError("42501", "AI_CONFIGURATION_FORBIDDEN")
		}
		ok, err := tx.tenantAdmin(ctx, tenant)
		if err != nil {
			return err
		}
		if !ok {
			return pgError("42501", "AI_CONFIGURATION_FORBIDDEN")
		}
		var provider string
		var enabled bool
		var digest, scope string
		err = tx.db.QueryRow(ctx, aiConsentConfigSQL, tenant).
			Scan(&provider, &enabled, &digest, &scope)
		if errors.Is(err, pgx.ErrNoRows) {
			return pgError("23514", "AI_DISABLED")
		}
		if err != nil {
			return err
		}
		now := timeutil.NowMicro()
		if arg.AllowCloud {
			// digest=="" cannot happen under PG (the guard always writes a sha256);
			// it means the SQLite port has not produced a digest yet, so consent must
			// be refused rather than matched against the empty column.
			if provider == "ollama" || !enabled || digest == "" ||
				arg.ConfigurationDigest != digest || arg.ScopeHash != scope {
				return pgError("23514", "AI_CLOUD_CONSENT_STALE")
			}
			if _, err = tx.db.Exec(ctx, aiConsentUpdateSQL, tenant,
				false, digest, scope, actor, now, nil); err != nil {
				return err
			}
		} else if _, err = tx.db.Exec(ctx, aiConsentUpdateSQL, tenant, true, "", "", actor, nil, now); err != nil {
			return err
		}
		// The consent columns are not part of the digest input, but PG recomputes
		// the digest on every UPDATE, so the port keeps that invariant.
		_, err = tx.refreshAIConfigurationDigest(ctx, tenant)
		return err
	})
}

const aiGovernanceSQL = `INSERT INTO ai_provider_configs(
 tenant_id,mode,provider,endpoint,model,encrypted_key,enabled,local_only,
 personal_mode,approved_endpoints,updated_at
)
VALUES(?1,'byo','openai','','','',0,1,?2,?3,(CAST(strftime('%s','now') AS INTEGER)*1000000))
ON CONFLICT(tenant_id) DO UPDATE SET
 personal_mode=excluded.personal_mode,
 approved_endpoints=excluded.approved_endpoints,
 updated_at=excluded.updated_at`

// PlatformConfigureTenantAIGovernance mirrors
// urmail_configure_tenant_ai_governance: it stores the tenant's personal-AI
// policy. approved_endpoints is a PG text[]; SQLite keeps it as JSON, and a nil
// slice becomes [] (PG's COALESCE(...,'{}')), never JSON null.
func (q *Queries) PlatformConfigureTenantAIGovernance(ctx context.Context, arg PlatformConfigureTenantAIGovernanceParams) error {
	switch arg.PersonalMode {
	case "disabled", "approved", "public":
	default:
		return pgError("23514", "AI_GOVERNANCE_INVALID")
	}
	endpoints := arg.ApprovedEndpoints
	if endpoints == nil {
		endpoints = []string{}
	}
	encoded, err := json.Marshal(endpoints)
	if err != nil {
		return pgError("23514", "AI_GOVERNANCE_INVALID")
	}
	return q.runTx(ctx, func(tx *Queries) error {
		tenant, actor, _, err := tx.sessionActor(ctx)
		if err != nil {
			return err
		}
		if tenant == 0 || actor == 0 {
			return pgError("42501", "AI_CONFIGURATION_FORBIDDEN")
		}
		ok, err := tx.tenantAdmin(ctx, tenant)
		if err != nil {
			return err
		}
		if !ok {
			return pgError("42501", "AI_CONFIGURATION_FORBIDDEN")
		}
		if _, err = tx.db.Exec(ctx, aiGovernanceSQL, tenant, arg.PersonalMode, string(encoded)); err != nil {
			return err
		}
		_, err = tx.refreshAIConfigurationDigest(ctx, tenant)
		return err
	})
}

// ---------------------------------------------------------------------------
// ai_provider_configs 的 configuration_digest：urmail_ai_configuration_consent_guard
// 触发器里的机械部分（S4 承接；role/consent 判定分支仍按 trigger-port.json 记
// app_pending）。
//
// PG 的算法是
//   encode(sha256(convert_to(jsonb_build_array(version,mode,provider,endpoint,
//                                          model,cloud_scope_hash)::text,'UTF8')),'hex')
// 其中 jsonb 文本渲染用 ", " 分隔元素。这个摘要决定云同意是否仍然有效：
// 配置身份变了而摘要没变，已授予的云同意就会被错误地当作继续有效，因此每条写
// 入 ai_provider_configs 的 Go 路径都要重算。
// ---------------------------------------------------------------------------

// jsonbTextString 复刻 PG jsonb 的字符串渲染（escape_json）：只转义 "、\ 与
// 命名控制字符，其余（含 DEL 与所有非 ASCII）原样输出 UTF-8。
func jsonbTextString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// aiConfigurationDigest is the Go port of the guard trigger's digest expression.
func aiConfigurationDigest(version int64, mode, provider, endpoint, model, scope string) string {
	var b strings.Builder
	b.WriteByte('[')
	b.WriteString(strconv.FormatInt(version, 10))
	for _, s := range []string{mode, provider, endpoint, model, scope} {
		b.WriteString(", ")
		b.WriteString(jsonbTextString(s))
	}
	b.WriteByte(']')
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

const aiConfigurationIdentitySQL = `SELECT configuration_version,mode,provider,endpoint,model,cloud_scope_hash
 FROM ai_provider_configs WHERE tenant_id=?1`

const aiConfigurationDigestSQL = `UPDATE ai_provider_configs SET configuration_digest=?2 WHERE tenant_id=?1`

// refreshAIConfigurationDigest recomputes configuration_digest from the stored
// row. It must run after the write (and after ai_config_version_keep_or_bump
// repaired the version) because the digest covers configuration_version.
func (q *Queries) refreshAIConfigurationDigest(ctx context.Context, tenant int64) (string, error) {
	var version int64
	var mode, provider, endpoint, model, scope string
	if err := q.db.QueryRow(ctx, aiConfigurationIdentitySQL, tenant).
		Scan(&version, &mode, &provider, &endpoint, &model, &scope); err != nil {
		return "", err
	}
	digest := aiConfigurationDigest(version, mode, provider, endpoint, model, scope)
	if _, err := q.db.Exec(ctx, aiConfigurationDigestSQL, tenant, digest); err != nil {
		return "", err
	}
	return digest, nil
}

const aiConsentResetSQL = `UPDATE ai_provider_configs SET
 tenant_local_only=1,cloud_consent_digest='',cloud_consent_scope_hash='',
 cloud_consent_by=?2,cloud_consent_at=NULL,
 cloud_consent_revoked_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1`

// aiSetProviderSQL is PlatformSetAIProvider after the sqlite conversion. It keeps
// PG's split between the platform snapshot, the enterprise snapshot and the base
// columns, including the "keep the old value when the request came from the
// other service" CASE arms.
const aiSetProviderSQL = `INSERT INTO ai_provider_configs(
 tenant_id,mode,provider,endpoint,model,encrypted_key,enabled,local_only,
 platform_provider,platform_endpoint,platform_model,platform_encrypted_key,platform_enabled,
 enterprise_mode,enterprise_provider,enterprise_endpoint,enterprise_model,
 enterprise_encrypted_key,enterprise_enabled,enterprise_local_only,
 active_service,input_micros_per_million,output_micros_per_million,cost_currency
)
VALUES(
 ?1,?2,?3,?4,?5,?6,?7,?8,
 CASE WHEN ?2='platform' THEN ?3 ELSE 'openai' END,
 CASE WHEN ?2='platform' THEN ?4 ELSE '' END,
 CASE WHEN ?2='platform' THEN ?5 ELSE '' END,
 CASE WHEN ?2='platform' THEN ?6 ELSE '' END,
 CASE WHEN ?2='platform' THEN ?7 ELSE 0 END,
 CASE WHEN ?2<>'platform' THEN ?2 ELSE 'byo' END,
 CASE WHEN ?2<>'platform' THEN ?3 ELSE 'openai' END,
 CASE WHEN ?2<>'platform' THEN ?4 ELSE '' END,
 CASE WHEN ?2<>'platform' THEN ?5 ELSE '' END,
 CASE WHEN ?2<>'platform' THEN ?6 ELSE '' END,
 CASE WHEN ?2<>'platform' THEN ?7 ELSE 0 END,
 CASE WHEN ?2<>'platform' THEN ?8 ELSE 1 END,
 CASE WHEN ?2='platform' THEN 'platform' ELSE 'enterprise' END,
 ?9,?10,COALESCE(NULLIF(?11,''),'CNY')
)
ON CONFLICT(tenant_id) DO UPDATE SET
 platform_provider=CASE WHEN excluded.mode='platform' THEN excluded.provider ELSE ai_provider_configs.platform_provider END,
 platform_endpoint=CASE WHEN excluded.mode='platform' THEN excluded.endpoint ELSE ai_provider_configs.platform_endpoint END,
 platform_model=CASE WHEN excluded.mode='platform' THEN excluded.model ELSE ai_provider_configs.platform_model END,
 platform_encrypted_key=CASE WHEN excluded.mode='platform' THEN excluded.encrypted_key ELSE ai_provider_configs.platform_encrypted_key END,
 platform_enabled=CASE WHEN excluded.mode='platform' THEN excluded.enabled ELSE ai_provider_configs.platform_enabled END,
 enterprise_mode=CASE WHEN excluded.mode<>'platform' THEN excluded.mode ELSE ai_provider_configs.enterprise_mode END,
 enterprise_provider=CASE WHEN excluded.mode<>'platform' THEN excluded.provider ELSE ai_provider_configs.enterprise_provider END,
 enterprise_endpoint=CASE WHEN excluded.mode<>'platform' THEN excluded.endpoint ELSE ai_provider_configs.enterprise_endpoint END,
 enterprise_model=CASE WHEN excluded.mode<>'platform' THEN excluded.model ELSE ai_provider_configs.enterprise_model END,
 enterprise_encrypted_key=CASE WHEN excluded.mode<>'platform' THEN excluded.encrypted_key ELSE ai_provider_configs.enterprise_encrypted_key END,
 enterprise_enabled=CASE WHEN excluded.mode<>'platform' THEN excluded.enabled ELSE ai_provider_configs.enterprise_enabled END,
 enterprise_local_only=CASE WHEN excluded.mode<>'platform' THEN excluded.local_only ELSE ai_provider_configs.enterprise_local_only END,
 active_service=CASE WHEN excluded.mode='platform' THEN 'platform' ELSE 'enterprise' END,
 mode=excluded.mode,
 provider=excluded.provider,
 endpoint=excluded.endpoint,
 model=excluded.model,
 encrypted_key=excluded.encrypted_key,
 enabled=excluded.enabled,
 local_only=excluded.local_only,
 input_micros_per_million=excluded.input_micros_per_million,
 output_micros_per_million=excluded.output_micros_per_million,
 cost_currency=excluded.cost_currency,
 updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)`

// PlatformSetAIProvider re-implements the generated statement only to refresh the
// digest in the same transaction; the RETURNING projection is re-read through
// PlatformGetAIProvider so the trigger-repaired row is what the caller sees.
func (q *Queries) PlatformSetAIProvider(ctx context.Context, arg PlatformSetAIProviderParams) (AiProviderConfigs, error) {
	var out AiProviderConfigs
	err := q.runTx(ctx, func(tx *Queries) error {
		if _, err := tx.db.Exec(ctx, aiSetProviderSQL, arg.TenantID, arg.Mode, arg.Provider,
			arg.Endpoint, arg.Model, arg.EncryptedKey, arg.Enabled, arg.LocalOnly,
			arg.InputMicrosPerMillion, arg.OutputMicrosPerMillion, arg.CostCurrency); err != nil {
			return err
		}
		if _, err := tx.refreshAIConfigurationDigest(ctx, arg.TenantID); err != nil {
			return err
		}
		var err error
		out, err = tx.PlatformGetAIProvider(ctx, arg.TenantID)
		return err
	})
	return out, err
}

const aiSetActiveServiceSQL = `UPDATE ai_provider_configs SET
 active_service=?1,
 mode=CASE WHEN ?1='platform' THEN 'platform' ELSE enterprise_mode END,
 provider=CASE WHEN ?1='platform' THEN platform_provider ELSE enterprise_provider END,
 endpoint=CASE WHEN ?1='platform' THEN platform_endpoint ELSE enterprise_endpoint END,
 model=CASE WHEN ?1='platform' THEN platform_model ELSE enterprise_model END,
 encrypted_key=CASE WHEN ?1='platform' THEN platform_encrypted_key ELSE enterprise_encrypted_key END,
 enabled=CASE WHEN ?1='platform' THEN platform_enabled ELSE enterprise_enabled END,
 local_only=CASE WHEN ?1='platform' THEN 0 ELSE enterprise_local_only END,
 updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?2`

// PlatformSetActiveAIService switches between the platform and enterprise
// snapshots. That rewrites mode/provider/endpoint/model, so the digest must be
// recomputed or an existing cloud consent would survive the switch.
func (q *Queries) PlatformSetActiveAIService(ctx context.Context, arg PlatformSetActiveAIServiceParams) error {
	return q.runTx(ctx, func(tx *Queries) error {
		if _, err := tx.db.Exec(ctx, aiSetActiveServiceSQL, arg.ActiveService, arg.TenantID); err != nil {
			return err
		}
		_, err := tx.refreshAIConfigurationDigest(ctx, arg.TenantID)
		return err
	})
}

// ---------------------------------------------------------------------------
// S5：PG advisory lock（8 处）的 SQLite 替代
//
// PG 有两种作用域：事务级（pg_advisory_xact_lock）与会话级（pg_advisory_lock /
// pg_advisory_unlock）。SQLite 没有 advisory lock：
//   - 事务级锁的用途是让同一键上的「读 → 判断 → 写」不被并发交叉。SQLite 只有
//     一个写者，且 sqlitedb.Open 以 _txlock=immediate 开启事务（见 open.go），
//     因此整个事务从 BEGIN 起就串行化，等价于事务级锁。这些方法保留为同一签名
//     的显式空操作，绝不静默消失：逐条登记在 scripts/sqlite/lock-semantics.md。
//   - 会话级草稿锁跨多个事务、要显式释放，不能靠事务串行化替代，故实现为进程内
//     按键互斥量。一个 SQLite 数据库由一个进程服务（单写者设计），进程作用域
//     即原 PG 的服务端作用域。
// ---------------------------------------------------------------------------

// LockMailboxEASSync（原 hashtextextended(tenant:mailbox:device,421932)）：
// 同一设备 EAS 同步由事务串行化互斥。
func (q *Queries) LockMailboxEASSync(_ context.Context, _ LockMailboxEASSyncParams) error { return nil }

// LockMailboxImportTenantBudget（原 hashtextextended('urmail/mailimport/'||tenant,0)）：
// 同一租户导入预算由事务串行化互斥。
func (q *Queries) LockMailboxImportTenantBudget(_ context.Context, _ int64) error { return nil }

// PlatformLockBillingTenant：租户计费/配额写入由事务串行化互斥。
func (q *Queries) PlatformLockBillingTenant(_ context.Context, _ int64) error { return nil }

// PlatformLockSendDraft（原 hashtextextended(tenant:mailbox:folder:validity:uid,974152)）：
// 草稿发送与草稿编辑互斥；PG 在事务级持锁，SQLite 用事务串行化。
func (q *Queries) PlatformLockSendDraft(_ context.Context, _ PlatformLockSendDraftParams) error {
	return nil
}

// PlatformLockDataImport（原 hashtextextended(tenant:mailbox:idempotency_key,782314)）：
// 同一幂等键的导入由事务串行化互斥。
func (q *Queries) PlatformLockDataImport(_ context.Context, _ PlatformLockDataImportParams) error {
	return nil
}

// PlatformLockRecallTenant：租户召回由事务串行化互斥。
func (q *Queries) PlatformLockRecallTenant(_ context.Context, _ int64) error { return nil }

// draftSessionLocks is the process-wide session lock registry. Keys are the
// same text PG hashed for hashtextextended(...,974152); the digest itself is
// irrelevant once the lock is not in the database.
var draftSessionLocks = struct {
	mu    sync.Mutex
	locks map[string]*draftSessionLock
}{locks: map[string]*draftSessionLock{}}

type draftSessionLock struct {
	token   chan struct{} // buffered(1); a token present means the lock is free
	held    bool          // owned by exactly one caller right now
	waiters int           // callers blocked on token, for map garbage collection
}

func draftSessionLockKey(tenant, mailbox, folder, validity, uid string) string {
	return tenant + ":" + mailbox + ":" + folder + ":" + validity + ":" + uid
}

// PlatformAcquireDraftSessionLock blocks until this process owns the key. It
// replaces pg_advisory_lock: the caller must pair it with
// PlatformReleaseDraftSessionLock, and cancellation while waiting is honored
// (PG's blocking acquire is cancelled by statement_timeout/context too).
func (q *Queries) PlatformAcquireDraftSessionLock(ctx context.Context, arg PlatformAcquireDraftSessionLockParams) error {
	key := draftSessionLockKey(arg.TenantID, arg.MailboxID, arg.Folder, arg.Validity, arg.Uid)
	reg := &draftSessionLocks
	reg.mu.Lock()
	l := reg.locks[key]
	if l == nil {
		l = &draftSessionLock{token: make(chan struct{}, 1)}
		l.token <- struct{}{}
		reg.locks[key] = l
	}
	l.waiters++
	reg.mu.Unlock()

	select {
	case <-l.token:
		reg.mu.Lock()
		l.waiters--
		l.held = true
		reg.mu.Unlock()
		return nil
	case <-ctx.Done():
		reg.mu.Lock()
		l.waiters--
		if !l.held && l.waiters == 0 {
			delete(reg.locks, key)
		}
		reg.mu.Unlock()
		return ctx.Err()
	}
}

// PlatformReleaseDraftSessionLock releases one acquire. It returns false when
// the key is not currently held, matching pg_advisory_unlock's boolean.
func (q *Queries) PlatformReleaseDraftSessionLock(_ context.Context, arg PlatformReleaseDraftSessionLockParams) (bool, error) {
	key := draftSessionLockKey(arg.TenantID, arg.MailboxID, arg.Folder, arg.Validity, arg.Uid)
	reg := &draftSessionLocks
	reg.mu.Lock()
	defer reg.mu.Unlock()
	l := reg.locks[key]
	if l == nil || !l.held {
		return false, nil
	}
	l.held = false
	if l.waiters > 0 {
		// Hand the free token to a blocked acquirer. The buffer is empty while
		// held, so this never blocks.
		l.token <- struct{}{}
	} else {
		// No holder and no waiter: drop the entry instead of keeping a token
		// nobody references.
		delete(reg.locks, key)
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Marketing report snapshots (S7).
//
// The PG versions were one bounded statement each: a `WITH ... AS MATERIALIZED`
// chain plus `jsonb_agg(to_jsonb(...) ORDER BY ...)`, with the report actor read
// from `current_setting('urmail.user_id')`. None of that survives a SQLite port
// (no MATERIALIZED, no session settings, different JSON aggregate), and the
// sqlc SQLite parser rejects the aggregate inside a CTE, so the snapshot is
// assembled here from the same bounded facts. The fact set, its ordering and
// the 10001-row probe are unchanged: `report.Build` still rejects >10000 rows,
// which the handler maps to 413.
// ---------------------------------------------------------------------------

// MarketingCampaignReportSnapshotParams is the parameter set for
// MarketingCampaignReportSnapshot.
type MarketingCampaignReportSnapshotParams struct {
	TenantID    int64
	ResourceID  int64
	ActorUserID int64
	FromTime    pgtype.Timestamptz
	UntilTime   pgtype.Timestamptz
}

// MarketingFlowReportSnapshotParams is the parameter set for
// MarketingFlowReportSnapshot.
type MarketingFlowReportSnapshotParams struct {
	TenantID    int64
	ResourceID  int64
	ActorUserID int64
	FromTime    pgtype.Timestamptz
	UntilTime   pgtype.Timestamptz
}

// reportWindowMicro converts the pgtype window into the microsecond bounds the
// schema stores. A NULL bound would compare as NULL in PG and yield no rows, so
// it is reported as ErrNoRows rather than silently scanning everything.
func reportWindowMicro(from, until pgtype.Timestamptz) (int64, int64, error) {
	if !from.Valid || !until.Valid {
		return 0, 0, pgx.ErrNoRows
	}
	return from.Time.UnixMicro(), until.Time.UnixMicro(), nil
}

// microValue normalizes a timestamp-ish scanned value. The same handwritten
// statement runs against PG (time.Time) today and against the SQLite INTEGER
// columns after the engine switch.
func microValue(v any) (int64, error) {
	switch t := v.(type) {
	case nil:
		return 0, nil
	case int64:
		return t, nil
	case time.Time:
		return t.UnixMicro(), nil
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n, nil
		}
		ts, err := time.Parse(time.RFC3339Nano, t)
		if err != nil {
			return 0, fmt.Errorf("report timestamp %q: %w", t, err)
		}
		return ts.UnixMicro(), nil
	case []byte:
		return microValue(string(t))
	}
	return 0, fmt.Errorf("report timestamp: unsupported scan type %T", v)
}

// boolValue normalizes a boolean-ish scanned value (PG bool, SQLite 0/1).
func boolValue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t != 0
	case float64:
		return t != 0
	case string:
		return t != "" && t != "0" && !strings.EqualFold(t, "false")
	case []byte:
		return boolValue(string(t))
	}
	return false
}

// reportFacts sorts the bounded fact set exactly like the PG statement's
// `ORDER BY record_kind,record_id` and keeps the 10001st row so Build can
// reject the snapshot.
func reportFacts(facts []report.Fact) []report.Fact {
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].RecordKind != facts[j].RecordKind {
			return facts[i].RecordKind < facts[j].RecordKind
		}
		return facts[i].RecordID < facts[j].RecordID
	})
	if len(facts) > report.MaxFacts+1 {
		facts = facts[:report.MaxFacts+1]
	}
	return facts
}

func reportPayload(name string, facts []report.Fact) ([]byte, error) {
	if facts == nil {
		facts = []report.Fact{}
	}
	return json.Marshal(struct {
		Name string        `json:"name"`
		Rows []report.Fact `json:"rows"`
	}{Name: name, Rows: facts})
}

// MarketingCampaignReportSnapshot renders one bounded campaign snapshot. The
// target SELECT doubles as the owner/admin authorization check the PG statement
// performed with the session user setting.
func (q *Queries) MarketingCampaignReportSnapshot(ctx context.Context, arg MarketingCampaignReportSnapshotParams) ([]byte, error) {
	from, until, err := reportWindowMicro(arg.FromTime, arg.UntilTime)
	if err != nil {
		return nil, err
	}
	var name string
	err = q.db.QueryRow(ctx, `SELECT c.name FROM marketing_campaigns c
WHERE c.tenant_id=$1 AND c.id=$2
AND EXISTS(SELECT 1 FROM user_tenant_roles role JOIN users actor ON actor.id=role.user_id
 WHERE role.tenant_id=c.tenant_id AND role.user_id=$3 AND role.role IN('owner','admin') AND actor.status='active')`,
		arg.TenantID, arg.ResourceID, arg.ActorUserID).Scan(&name)
	if err != nil {
		return nil, err
	}
	facts := []report.Fact{}
	rows, err := q.db.Query(ctx, `SELECT r.id, r.email, r.status, COALESCE(r.result_code,''), r.created_at
FROM marketing_recipients r
WHERE r.tenant_id=$1 AND r.campaign_id=$2 AND r.created_at>=$3 AND r.created_at<$4`,
		arg.TenantID, arg.ResourceID, from, until)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, created int64
		var email, status, result string
		if err := rows.Scan(&id, &email, &status, &result, &created); err != nil {
			rows.Close()
			return nil, err
		}
		facts = append(facts, report.Fact{
			RecordKind: "recipient", RecordID: strconv.FormatInt(id, 10), InstanceID: id,
			Email: email, State: status, ResultCode: result,
			OccurredAt: time.UnixMicro(created).UTC(), Weight: 1,
			Uncertain: status == "uncertain" || status == "submitting",
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.db.Query(ctx, `SELECT f.channel_id, f.event_id, f.status, f.created_at, r.id, r.email
FROM marketing_feedback f JOIN marketing_recipients r ON r.tenant_id=f.tenant_id AND r.id=f.recipient_id
WHERE f.tenant_id=$1 AND r.campaign_id=$2 AND r.created_at>=$3 AND r.created_at<$4`,
		arg.TenantID, arg.ResourceID, from, until)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var channel, recipient, created int64
		var eventID, status, email string
		if err := rows.Scan(&channel, &eventID, &status, &created, &recipient, &email); err != nil {
			rows.Close()
			return nil, err
		}
		facts = append(facts, report.Fact{
			RecordKind: "feedback", RecordID: strconv.FormatInt(channel, 10) + ":" + eventID,
			InstanceID: recipient, Email: email, State: status,
			OccurredAt: time.UnixMicro(created).UTC(), Weight: 1,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	suppressed, err := q.marketingSuppressed(ctx, arg.TenantID)
	if err != nil {
		return nil, err
	}
	for i := range facts {
		if facts[i].RecordKind == "recipient" && suppressed[facts[i].Email] {
			facts[i].Suppressed = true
		}
	}
	return reportPayload(name, reportFacts(facts))
}

// MarketingFlowReportSnapshot renders one bounded flow snapshot (entries,
// positions and node executions).
func (q *Queries) MarketingFlowReportSnapshot(ctx context.Context, arg MarketingFlowReportSnapshotParams) ([]byte, error) {
	from, until, err := reportWindowMicro(arg.FromTime, arg.UntilTime)
	if err != nil {
		return nil, err
	}
	var name string
	err = q.db.QueryRow(ctx, `SELECT d.name FROM marketing_flow_definitions d
WHERE d.tenant_id=$1 AND d.id=$2
AND EXISTS(SELECT 1 FROM user_tenant_roles role JOIN users actor ON actor.id=role.user_id
 WHERE role.tenant_id=d.tenant_id AND role.user_id=$3 AND role.role IN('owner','admin') AND actor.status='active')`,
		arg.TenantID, arg.ResourceID, arg.ActorUserID).Scan(&name)
	if err != nil {
		return nil, err
	}
	suppressed, err := q.marketingSuppressed(ctx, arg.TenantID)
	if err != nil {
		return nil, err
	}
	facts := []report.Fact{}
	rows, err := q.db.Query(ctx, `SELECT e.id, e.source, e.event_key, e.created_at, e.recipient_count, v.version
FROM marketing_flow_entries e
JOIN marketing_flow_versions v ON v.tenant_id=e.tenant_id AND v.id=e.version_id AND v.definition_id=e.definition_id
WHERE e.tenant_id=$1 AND e.definition_id=$2 AND e.created_at>=$3 AND e.created_at<$4`,
		arg.TenantID, arg.ResourceID, from, until)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, created, count, version int64
		var source, eventKey string
		if err := rows.Scan(&id, &source, &eventKey, &created, &count, &version); err != nil {
			rows.Close()
			return nil, err
		}
		facts = append(facts, report.Fact{
			RecordKind: "entry", RecordID: strconv.FormatInt(id, 10), Version: int32(version),
			State: source, ResultCode: eventKey, OccurredAt: time.UnixMicro(created).UTC(), Weight: count,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.db.Query(ctx, `SELECT p.id, p.email, p.stopped_at, p.unsubscribed, p.delivery_uncertain, p.wait_until,
 p.last_error, e.version, e.created_at, f.status, s.kind
FROM marketing_flow_positions p
JOIN marketing_flow_entries e ON e.tenant_id=p.tenant_id AND e.id=p.entry_id
JOIN marketing_flows f ON f.tenant_id=p.tenant_id AND f.id=p.flow_id
JOIN marketing_flow_steps s ON s.tenant_id=p.tenant_id AND s.flow_id=p.flow_id AND s.step_key=p.step_key
WHERE p.tenant_id=$1 AND e.definition_id=$2 AND e.created_at>=$3 AND e.created_at<$4`,
		arg.TenantID, arg.ResourceID, from, until)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMicro()
	for rows.Next() {
		var id, version int64
		var email, lastError, flowStatus, stepKind string
		var stopped, unsubscribed, uncertain, waitUntil, entered any
		if err := rows.Scan(&id, &email, &stopped, &unsubscribed, &uncertain, &waitUntil,
			&lastError, &version, &entered, &flowStatus, &stepKind); err != nil {
			rows.Close()
			return nil, err
		}
		state := "pending"
		waitMicro, err := microValue(waitUntil)
		if err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case stopped != nil:
			state = "stopped"
		case boolValue(unsubscribed):
			state = "unsubscribed"
		case boolValue(uncertain):
			state = "uncertain"
		case stepKind == "stop":
			state = "completed"
		case flowStatus != "active":
			state = "paused"
		case lastError != "":
			state = "failed"
		case waitUntil != nil && waitMicro > now:
			state = "waiting"
		}
		enteredMicro, err := microValue(entered)
		if err != nil {
			rows.Close()
			return nil, err
		}
		facts = append(facts, report.Fact{
			RecordKind: "instance", RecordID: strconv.FormatInt(id, 10), Version: int32(version),
			InstanceID: id, Email: email, State: state, ResultCode: lastError,
			OccurredAt: time.UnixMicro(enteredMicro).UTC(), Weight: 1,
			Suppressed: suppressed[email], Uncertain: boolValue(uncertain),
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.db.Query(ctx, `SELECT x.id, p.version, p.id, p.email, x.status, x.error_code, x.created_at
FROM marketing_flow_node_executions x
JOIN marketing_flow_positions p ON p.tenant_id=x.tenant_id AND p.id=x.position_id
JOIN marketing_flow_entries e ON e.tenant_id=p.tenant_id AND e.id=p.entry_id
WHERE x.tenant_id=$1 AND e.definition_id=$2 AND e.created_at>=$3 AND e.created_at<$4`,
		arg.TenantID, arg.ResourceID, from, until)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, version, position, created int64
		var email, status, errorCode string
		if err := rows.Scan(&id, &version, &position, &email, &status, &errorCode, &created); err != nil {
			rows.Close()
			return nil, err
		}
		facts = append(facts, report.Fact{
			RecordKind: "execution", RecordID: strconv.FormatInt(id, 10), Version: int32(version),
			InstanceID: position, Email: email, State: status, ResultCode: errorCode,
			OccurredAt: time.UnixMicro(created).UTC(), Weight: 1,
			Uncertain: status == "uncertain",
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return reportPayload(name, reportFacts(facts))
}

// marketingSuppressed returns the tenant's suppressed addresses; the PG
// statements tested suppression with a correlated EXISTS per fact.
func (q *Queries) marketingSuppressed(ctx context.Context, tenant int64) (map[string]bool, error) {
	rows, err := q.db.Query(ctx, `SELECT DISTINCT email FROM marketing_suppressions WHERE tenant_id=$1`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		out[email] = true
	}
	return out, rows.Err()
}

// SetMarketingReportBudget is the SQLite replacement for the PG statement-level
// `set_config('statement_timeout','5000ms',true)`. SQLite has no per-statement
// timeout; the bounded window and the 10001-row probe already cap the work, and
// the connection-level busy_timeout bounds waiting. Keeping the method (rather
// than deleting it) preserves the handler's fail-closed sequence.
func (q *Queries) SetMarketingReportBudget(ctx context.Context) error { return nil }
