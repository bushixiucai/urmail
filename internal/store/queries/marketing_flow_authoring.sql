-- name: ListMarketingFlowDefinitions :many
-- scope: tenant

SELECT id,name,draft_revision,status,published_version,created_at,updated_at FROM marketing_flow_definitions WHERE tenant_id=?1 ORDER BY id DESC LIMIT 100;

-- name: CountMarketingFlowDefinitions :one
-- scope: tenant

SELECT count(*) FROM marketing_flow_definitions WHERE tenant_id=?1;

-- name: CreateMarketingFlowDefinition :one
-- scope: tenant

INSERT INTO marketing_flow_definitions(tenant_id,name,draft) VALUES(?1,?2,?3) RETURNING id, tenant_id, name, draft, draft_revision, status, published_version, created_at, updated_at;

-- name: GetMarketingFlowDefinition :one
-- scope: tenant

SELECT id, tenant_id, name, draft, draft_revision, status, published_version, created_at, updated_at FROM marketing_flow_definitions WHERE tenant_id=?1 AND id=?2;

-- name: UpdateMarketingFlowDefinition :one
-- scope: tenant

UPDATE marketing_flow_definitions SET name=?3,draft=?4,draft_revision=draft_revision+1,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND id=?2 AND draft_revision=?5 RETURNING id, tenant_id, name, draft, draft_revision, status, published_version, created_at, updated_at;

-- name: GetMarketingFlowVersionForRevision :one
-- scope: tenant

SELECT id, tenant_id, definition_id, version, draft_revision, runtime_flow_id, definition, published_by, created_at FROM marketing_flow_versions WHERE tenant_id=?1 AND definition_id=?2 AND draft_revision=?3;

-- name: GetMarketingFlowVersion :one
-- scope: tenant

SELECT id, tenant_id, definition_id, version, draft_revision, runtime_flow_id, definition, published_by, created_at FROM marketing_flow_versions WHERE tenant_id=?1 AND definition_id=?2 AND version=?3;

-- name: CreateMarketingRuntimeFlow :one
-- scope: tenant

INSERT INTO marketing_flows(tenant_id,name,status) VALUES(?1,?2,'active') RETURNING id;

-- name: CreateMarketingRuntimeStep :exec
-- scope: tenant

INSERT INTO marketing_flow_steps(tenant_id,flow_id,step_key,kind,next_key,opened_key,position,wait_seconds,condition_field,condition_operator,condition_value)
VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11);

-- name: GetMarketingFlowTemplate :one
-- scope: tenant

SELECT id,subject,text_body FROM marketing_templates WHERE tenant_id=?1 AND id=?2;

-- name: CreateMarketingFlowContentSnapshot :exec
-- scope: tenant

INSERT INTO marketing_flow_content_snapshots(tenant_id,runtime_flow_id,step_key,template_id,subject,text_body) VALUES(?1,?2,?3,?4,?5,?6);

-- name: CreateMarketingFlowVersion :one
-- scope: tenant

INSERT INTO marketing_flow_versions(tenant_id,definition_id,version,draft_revision,runtime_flow_id,definition,published_by)
VALUES(?1,?2,?3,?4,?5,?6,?7) RETURNING id, tenant_id, definition_id, version, draft_revision, runtime_flow_id, definition, published_by, created_at;

-- name: PublishMarketingFlowDefinition :exec
-- scope: tenant

UPDATE marketing_flow_definitions SET status='active',published_version=?3,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE tenant_id=?1 AND id=?2;

-- name: PauseMarketingFlowDefinition :exec
-- scope: tenant

UPDATE marketing_flow_definitions SET status='paused',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE tenant_id=?1 AND id=?2;

-- name: PauseMarketingRuntimeFlows :exec
-- scope: tenant

UPDATE marketing_flows SET status='paused' WHERE marketing_flows.tenant_id=?1 AND marketing_flows.id IN(SELECT v.runtime_flow_id FROM marketing_flow_versions v WHERE v.tenant_id=?1 AND v.definition_id=?2);
-- name: ListMarketingFlowVersions :many
-- scope: tenant

SELECT id,version,draft_revision,runtime_flow_id,published_by,created_at FROM marketing_flow_versions
WHERE tenant_id=?1 AND definition_id=?2 AND (sqlc.arg(before_id)=0 OR id<sqlc.arg(before_id)) ORDER BY id DESC LIMIT 51;

-- name: GetMarketingFlowEntry :one
-- scope: tenant

SELECT id, tenant_id, definition_id, version_id, event_key, source, list_id, recipient_count, created_at FROM marketing_flow_entries WHERE tenant_id=?1 AND definition_id=?2 AND event_key=?3;

-- name: GetMarketingFlowList :one
-- scope: tenant

SELECT id FROM marketing_lists WHERE tenant_id=?1 AND id=?2;

-- name: ListMarketingFlowEligibleContacts :many
-- scope: tenant

SELECT c.email FROM marketing_contacts c WHERE c.tenant_id=?1 AND c.list_id=?2 AND c.opted_in
AND NOT EXISTS(SELECT 1 FROM marketing_suppressions s WHERE s.tenant_id=c.tenant_id AND s.email=c.email)
AND c.email NOT IN (SELECT p.email FROM marketing_flow_positions p WHERE p.tenant_id=c.tenant_id AND p.flow_id=sqlc.arg(flow_id) AND p.email=c.email)
ORDER BY c.email LIMIT 1001;

-- name: CountMarketingFlowLiveInstances :one
-- scope: tenant

SELECT count(*) FROM marketing_flow_positions p JOIN marketing_flow_versions v ON v.tenant_id=p.tenant_id AND v.runtime_flow_id=p.flow_id
JOIN marketing_flow_steps s ON s.tenant_id=p.tenant_id AND s.flow_id=p.flow_id AND s.step_key=p.step_key
WHERE p.tenant_id=?1 AND NOT p.unsubscribed AND p.stopped_at IS NULL AND s.kind<>'stop';

-- name: CreateMarketingFlowEntry :one
-- scope: tenant

INSERT INTO marketing_flow_entries(tenant_id,definition_id,version_id,event_key,source,list_id,recipient_count)
VALUES(?1,?2,?3,?4,'contact_enrolled',?5,?6) RETURNING id, tenant_id, definition_id, version_id, event_key, source, list_id, recipient_count, created_at;

-- name: CreateMarketingFlowInstance :execrows
-- scope: tenant

INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key,entry_id,event_facts)
VALUES(?1,?2,?3,?4,?5,?6) ON CONFLICT(tenant_id,flow_id,email) DO NOTHING;

-- name: ListMarketingFlowInstances :many
-- scope: tenant

SELECT p.id, p.email, p.step_key, p.unsubscribed, coalesce(p.wait_until, 0) AS wait_until, p.delivery_uncertain, coalesce(p.lease_until, 0) AS lease_until, p.last_error, p.next_attempt_at, p.attempt_count, coalesce(p.stopped_at, 0) AS stopped_at, p.updated_at, v.version, f.status AS flow_status, s.kind
FROM marketing_flow_positions p JOIN marketing_flow_versions v ON v.tenant_id=p.tenant_id AND v.runtime_flow_id=p.flow_id
JOIN marketing_flows f ON f.tenant_id=p.tenant_id AND f.id=p.flow_id
JOIN marketing_flow_steps s ON s.tenant_id=p.tenant_id AND s.flow_id=p.flow_id AND s.step_key=p.step_key
WHERE p.tenant_id=?1 AND v.definition_id=?2 AND (sqlc.arg(before_id)=0 OR p.id<sqlc.arg(before_id))
AND (sqlc.arg(email)='' OR p.email=sqlc.arg(email))
ORDER BY p.id DESC LIMIT 101;

-- name: ListMarketingFlowNodeExecutions :many
-- scope: tenant

SELECT e.node_key,e.lease_fence,e.status,e.error_code,e.created_at FROM marketing_flow_node_executions e
JOIN marketing_flow_positions p ON p.tenant_id=e.tenant_id AND p.id=e.position_id
JOIN marketing_flow_versions v ON v.tenant_id=p.tenant_id AND v.runtime_flow_id=p.flow_id
WHERE e.tenant_id=?1 AND v.definition_id=?2 AND e.position_id=?3 ORDER BY e.id DESC LIMIT 100;

-- name: StopMarketingFlowInstance :execrows
-- scope: tenant

UPDATE marketing_flow_positions SET stopped_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),lease_fence=lease_fence+1,lease_owner=NULL,lease_until=NULL,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE marketing_flow_positions.tenant_id=?1 AND marketing_flow_positions.id=?3 AND stopped_at IS NULL
AND EXISTS(SELECT 1  WHERE EXISTS(SELECT 1 FROM marketing_flow_versions v WHERE v.tenant_id=marketing_flow_positions.tenant_id AND v.runtime_flow_id=marketing_flow_positions.flow_id AND v.definition_id=?2));

-- name: GetMarketingFlowInstance :one
-- scope: tenant

SELECT p.id FROM marketing_flow_positions p JOIN marketing_flow_versions v ON v.tenant_id=p.tenant_id AND v.runtime_flow_id=p.flow_id
WHERE p.tenant_id=?1 AND v.definition_id=?2 AND p.id=?3;

-- name: ListMarketingFlowPublishedContent :many
-- scope: tenant

SELECT step_key,subject,text_body FROM marketing_flow_content_snapshots WHERE tenant_id=?1 AND runtime_flow_id=?2 ORDER BY step_key;

-- 中文注记（sqlc 的 sqlite 引擎按字节偏移切分语句，任何非 ASCII 注记都必须放在文件末尾）：
-- StopMarketingFlowInstance: RetryMarketingFlowInstance 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- StopMarketingFlowInstance: urmail_retry_marketing_flow_instance = 平台角色或管理员 / 版本定义绑定 / 租约过期 + 失败或确认未接收 / 释放预留 / 复位位置
-- ListMarketingFlowPublishedContent: RecordMarketingFlowAudit 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- ListMarketingFlowPublishedContent: urmail_record_marketing_flow_audit = 管理员 + 定义存在 + 动作白名单 + jsonb 对象与 4000 字节上限 / audit_logs 写入
