-- name: ListDNSCredentials :many
-- scope: tenant

SELECT id,tenant_id,domain_id,provider,zone_name,zone_id,version,enabled,verified_at,revoked_at,updated_at FROM dns_credentials WHERE tenant_id=?1 AND domain_id=?2;





-- name: ListDNSPlans :many
-- scope: tenant

SELECT * FROM dns_change_plans WHERE tenant_id=?1 AND domain_id=?2 ORDER BY created_at DESC,id LIMIT 20;
-- name: GetDNSPlan :one
-- scope: tenant

SELECT * FROM dns_change_plans WHERE tenant_id=?1 AND id=?2;
-- name: ListDNSActions :many
-- scope: tenant

SELECT * FROM dns_record_actions WHERE tenant_id=?1 AND plan_id=?2 ORDER BY ordinal;

-- name: GetDomainProvisioning :one
-- scope: tenant

SELECT provisioning_state,dns_explicit FROM domains WHERE tenant_id=?1 AND id=?2;

-- 中文注记（sqlc 的 sqlite 引擎按字节偏移切分语句，任何非 ASCII 注记都必须放在文件末尾）：
-- ListDNSCredentials: SaveDNSCredential 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- ListDNSCredentials: urmail_save_dns_credential = dns_actor 授权 + zone/密文校验 + upsert 版本自增 + 失效活动计划（事务）
-- ListDNSCredentials: RevokeDNSCredential 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- ListDNSCredentials: urmail_revoke_dns_credential = dns_actor 授权 + 凭据吊销 + 失效活动计划（事务）
-- ListDNSCredentials: CreateDNSPlan 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- ListDNSCredentials: urmail_create_dns_plan = dns_actor 授权 + kind/凭据版本校验 + 15 分钟预览计划插入
-- ListDNSCredentials: ConfirmDNSPlan 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- ListDNSCredentials: urmail_confirm_dns_plan = 计划行锁读 + digest/过期/conflicts/凭据校验 + 确认
-- ListDNSCredentials: CancelDNSPlan 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- ListDNSCredentials: urmail_cancel_dns_plan = 行锁读 + 状态/理由校验 + 取消
-- ListDNSActions: RequestOnboardingRetry 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- ListDNSActions: urmail_request_onboarding = dns_actor 授权 + 恢复对账门 + 任务取代/新建 + provisioning 重置；subscriptions 条款随 0156 删除，登记 app_pending
-- GetDomainProvisioning: RequestDNSRollback 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- GetDomainProvisioning: urmail_rollback_dns_plan = 行锁读 + completed/理由/非 manual 校验 + 回滚排队
-- GetDomainProvisioning: ReviewDNSPlan 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- GetDomainProvisioning: urmail_review_dns_plan = 行锁读 + uncertain/理由校验 + 计划完成 + 未拥有动作置 applied/owned=0
-- GetDomainProvisioning: AbandonDNSPlan 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- GetDomainProvisioning: urmail_abandon_dns_plan = 行锁读 + uncertain|failed/理由校验 + 显式放弃
