-- name: ListMarketingChannels :many
SELECT id,tenant_id,name,kind,enabled,created_at FROM marketing_channels WHERE tenant_id=$1 ORDER BY id;

-- name: ListMarketingTemplates :many
SELECT * FROM marketing_templates WHERE tenant_id=$1 ORDER BY id DESC LIMIT 100;

-- name: CreateMarketingTemplate :one
INSERT INTO marketing_templates(tenant_id,name,subject,text_body) VALUES($1,$2,$3,$4) RETURNING *;

-- name: ListMarketingLists :many
SELECT * FROM marketing_lists WHERE tenant_id=$1 ORDER BY id DESC LIMIT 100;

-- name: CreateMarketingList :one
INSERT INTO marketing_lists(tenant_id,name) VALUES($1,$2) RETURNING *;

-- name: UpsertMarketingContact :one
INSERT INTO marketing_contacts(tenant_id,list_id,email,consent_source,opted_in) VALUES($1,$2,$3,$4,$5)
ON CONFLICT(tenant_id,list_id,email) DO UPDATE SET opted_in=EXCLUDED.opted_in,consent_source=EXCLUDED.consent_source,consent_at=now() RETURNING *;

-- name: ListMarketingContacts :many
SELECT * FROM marketing_contacts WHERE tenant_id=$1 AND list_id=$2 ORDER BY email LIMIT 1000;

-- name: CreateMarketingCampaign :one
INSERT INTO marketing_campaigns(tenant_id,name,channel_id,template_id,list_id) VALUES($1,$2,$3,$4,$5) RETURNING *;

-- name: ListMarketingCampaigns :many
SELECT * FROM marketing_campaigns WHERE tenant_id=$1 ORDER BY id DESC LIMIT 100;

-- name: GetMarketingCampaign :one
SELECT * FROM marketing_campaigns WHERE tenant_id=$1 AND id=$2;

-- name: ScheduleMarketingCampaign :one
SELECT urmail_schedule_marketing(@tenant_id::bigint,@campaign_id::bigint,@scheduled_at::timestamptz)::text AS result;

-- name: ChangeMarketingCampaignState :one
UPDATE marketing_campaigns SET status=$3 WHERE tenant_id=$1 AND id=$2 AND
 (($3='paused' AND status IN('scheduled','running')) OR ($3='scheduled' AND status='paused') OR ($3='canceled' AND status IN('draft','scheduled','running','paused')))
RETURNING *;

-- name: CancelQueuedMarketingRecipients :execrows
UPDATE marketing_recipients SET status='canceled',completed_at=now(),result_code='campaign_canceled'
WHERE tenant_id=$1 AND campaign_id=$2 AND status IN('queued','leased');

-- name: ListMarketingRecipients :many
SELECT * FROM marketing_recipients WHERE tenant_id=$1 AND campaign_id=$2 ORDER BY id LIMIT 1000;

-- name: PreviewMarketingCampaign :one
SELECT c.id,c.tenant_id,c.name,c.status,t.subject,t.text_body,
 (SELECT count(*) FROM marketing_contacts mc WHERE mc.tenant_id=c.tenant_id AND mc.list_id=c.list_id AND mc.opted_in) AS opted_in_count,
 (SELECT count(*) FROM marketing_contacts mc JOIN marketing_suppressions s ON s.tenant_id=mc.tenant_id AND s.email=mc.email WHERE mc.tenant_id=c.tenant_id AND mc.list_id=c.list_id AND mc.opted_in) AS suppressed_count
FROM marketing_campaigns c JOIN marketing_templates t ON t.tenant_id=c.tenant_id AND t.id=c.template_id WHERE c.tenant_id=$1 AND c.id=$2;
