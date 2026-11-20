-- Platform-side daily-sent usage queries used by the SMTP policy service
-- (internal/mail/policy). The policy service resolves the tenant from the
-- mailbox address itself and runs outside a tenant context, so these queries
-- are exempt from the tenant_id predicate per AGENTS.md and therefore live in
-- a platform_*.sql file (M3 review P1-2).
-- (moved verbatim from usage.sql)

-- GetMailboxDailySent 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- 业务时区的 day key 需 Go 侧时区库；PG 复用 urmail_quota_day。

-- GetMailboxDailySentForUpdate 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- 同上（day key 由 Go 计算；SQLite 无行锁，事务串行）。

-- IncrementMailboxDailySent 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- 同上（day key 由 Go 计算）。

-- name: GetTenantIDBySenderDomain :one
-- scope: tenant

-- Platform-only collector resolution; sender domain is matched to active owned domain.
SELECT tenant_id FROM domains WHERE fqdn = ?1 AND status = 'active';

-- name: GetDeliveryEventTenantByQueueID :one
-- scope: tenant

-- Platform-only correlation lets recipient status lines inherit the tenant
-- from the earlier queue acceptance event without exposing cross-tenant APIs.
-- Queue IDs are only unique per Postfix instance, so the instance name is
-- mandatory: without it a shared queue id resolves to whichever tenant
-- happened to insert last. The preference order keeps queue-acceptance rows
-- (which carry the sender) ahead of later recipient status rows.
SELECT tenant_id, sender, sender_domain, message_id
FROM delivery_events
WHERE queue_id = ?1 AND instance = ?2
ORDER BY (status = 'queued') DESC, occurred_at DESC
LIMIT 1;

-- name: PlatformInboundAllowed :one
-- scope: platform

SELECT COALESCE((SELECT EXISTS(SELECT 1 FROM tenants rt WHERE rt.id=d.tenant_id AND rt.status='active') AND EXISTS(SELECT 1 FROM domains rd WHERE rd.tenant_id=d.tenant_id AND rd.id=d.id AND rd.status='active' AND rd.verified_at IS NOT NULL AND rd.provisioning_state='ready') FROM domains d WHERE d.fqdn=substr(?1, instr(?1,'@')+1)),true) AS allowed;

-- name: PlatformCommissionRate :one
-- scope: platform

SELECT commission_bps FROM channels WHERE id=?1;

-- PlatformTakeMailboxDailyQuota 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- urmail_take_mailbox_quota = 多步配额校验 + 时区日 + 原子自增。

