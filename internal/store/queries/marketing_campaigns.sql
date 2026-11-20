-- name: ListMarketingChannels :many
-- scope: tenant

SELECT id,tenant_id,name,kind,enabled,created_at FROM marketing_channels WHERE tenant_id=?1 ORDER BY id;

-- name: ListMarketingTemplates :many
-- scope: tenant

SELECT * FROM marketing_templates WHERE tenant_id=?1 ORDER BY id DESC LIMIT 100;

-- name: CreateMarketingTemplate :one
-- scope: tenant

INSERT INTO marketing_templates(tenant_id,name,subject,text_body) VALUES(?1,?2,?3,?4) RETURNING *;

-- name: ListMarketingLists :many
-- scope: tenant

SELECT * FROM marketing_lists WHERE tenant_id=?1 ORDER BY id DESC LIMIT 100;

-- name: CreateMarketingList :one
-- scope: tenant

INSERT INTO marketing_lists(tenant_id,name) VALUES(?1,?2) RETURNING *;

-- name: UpsertMarketingContact :one
-- scope: tenant

INSERT INTO marketing_contacts(tenant_id,list_id,email,consent_source,opted_in) VALUES(?1,?2,?3,?4,?5)
ON CONFLICT(tenant_id,list_id,email) DO UPDATE SET opted_in=EXCLUDED.opted_in,consent_source=EXCLUDED.consent_source,consent_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) RETURNING *;

-- name: ListMarketingContacts :many
-- scope: tenant

SELECT * FROM marketing_contacts WHERE tenant_id=?1 AND list_id=?2 ORDER BY email LIMIT 1000;

-- name: CreateMarketingCampaign :one
-- scope: tenant

INSERT INTO marketing_campaigns(tenant_id,name,channel_id,template_id,list_id) VALUES(?1,?2,?3,?4,?5) RETURNING *;

-- name: ListMarketingCampaigns :many
-- scope: tenant

SELECT * FROM marketing_campaigns WHERE tenant_id=?1 ORDER BY id DESC LIMIT 100;

-- name: GetMarketingCampaign :one
-- scope: tenant

SELECT * FROM marketing_campaigns WHERE tenant_id=?1 AND id=?2;


-- name: ChangeMarketingCampaignState :one
-- scope: tenant

UPDATE marketing_campaigns SET status=?3 WHERE tenant_id=?1 AND id=?2 AND
 ((?3='paused' AND status IN('scheduled','running')) OR (?3='scheduled' AND status='paused') OR (?3='canceled' AND status IN('draft','scheduled','running','paused')))
RETURNING *;

-- name: CancelQueuedMarketingRecipients :execrows
-- scope: tenant

UPDATE marketing_recipients SET status='canceled',completed_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),result_code='campaign_canceled'
WHERE tenant_id=?1 AND campaign_id=?2 AND status IN('queued','leased');

-- name: ListMarketingRecipients :many
-- scope: tenant

SELECT * FROM marketing_recipients WHERE tenant_id=?1 AND campaign_id=?2 ORDER BY id LIMIT 1000;

-- MKT-005 outcome report. Counters come from the incremental
-- marketing_campaign_stats row; pending/total come from recipient facts.
-- name: GetMarketingCampaignReport :one
-- scope: tenant

SELECT c.id AS campaign_id,c.name,c.status,
 COALESCE(s.sent,0) AS sent,COALESCE(s.failed,0) AS failed,
 COALESCE(s.uncertain,0) AS uncertain,COALESCE(s.unsubscribed,0) AS unsubscribed,
 COALESCE(s.bounced,0) AS bounced,COALESCE(s.bounced_unknown,0) AS bounced_unknown,
 (SELECT count(*) FROM marketing_recipients r WHERE r.tenant_id=c.tenant_id AND r.campaign_id=c.id) AS total_recipients,
 (SELECT count(*) FROM marketing_recipients r WHERE r.tenant_id=c.tenant_id AND r.campaign_id=c.id AND r.status IN('queued','leased','submitting')) AS pending_recipients
FROM marketing_campaigns c LEFT JOIN marketing_campaign_stats s ON s.tenant_id=c.tenant_id AND s.campaign_id=c.id
WHERE c.tenant_id=?1 AND c.id=?2;

-- name: PreviewMarketingCampaign :one
-- scope: tenant

SELECT c.id,c.tenant_id,c.name,c.status,t.subject,t.text_body,
 (SELECT count(*) FROM marketing_contacts mc WHERE mc.tenant_id=c.tenant_id AND mc.list_id=c.list_id AND mc.opted_in) AS opted_in_count,
 (SELECT count(*) FROM marketing_contacts mc JOIN marketing_suppressions s ON s.tenant_id=mc.tenant_id AND s.email=mc.email WHERE mc.tenant_id=c.tenant_id AND mc.list_id=c.list_id AND mc.opted_in) AS suppressed_count
FROM marketing_campaigns c JOIN marketing_templates t ON t.tenant_id=c.tenant_id AND t.id=c.template_id WHERE c.tenant_id=?1 AND c.id=?2;

-- 中文注记（sqlc 的 sqlite 引擎按字节偏移切分语句，任何非 ASCII 注记都必须放在文件末尾）：
-- GetMarketingCampaign: ScheduleMarketingCampaign 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- GetMarketingCampaign: urmail_schedule_marketing = 管理员授权 + 模板快照/排期 + 收件人去重入队（opted_in 且非抑制）；day_limit/通道/发件人前置校验返回状态串
