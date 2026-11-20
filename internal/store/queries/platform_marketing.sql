-- name: PlatformCreateMarketingChannel :one
-- scope: platform

INSERT INTO marketing_channels(tenant_id,name,kind,sender_address,encrypted_config,enabled) VALUES(?1,?2,?3,?4,?5,?6) RETURNING id,tenant_id,name,kind,enabled,created_at;

-- name: PlatformGetMarketingChannel :one
-- scope: platform

SELECT * FROM marketing_channels WHERE id=?1;

-- name: PlatformGetActiveMarketingChannel :one
-- scope: platform

SELECT id,tenant_id,name,kind,encrypted_config FROM marketing_channels
WHERE tenant_id=?1 AND enabled AND kind='dedicated_gateway' ORDER BY id LIMIT 1;

-- name: PlatformRecoverMarketingSubmissions :execrows
-- scope: platform

UPDATE marketing_recipients SET status='uncertain',result_code='lease_expired_inflight',completed_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),fence=fence+1,lease_until=NULL
WHERE status='submitting' AND lease_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: PlatformRecoverMarketingClaims :execrows
-- scope: platform

UPDATE marketing_recipients SET status='queued',fence=fence+1,lease_owner=NULL,lease_until=NULL
WHERE status='leased' AND lease_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: PlatformAdvanceMarketingTurn :execrows
-- scope: platform

UPDATE platform_marketing_dispatch SET last_turn=(SELECT COALESCE(MAX(last_turn), 0) + 1 FROM platform_marketing_dispatch)
WHERE EXISTS(
 SELECT 1 FROM marketing_recipients r JOIN marketing_campaigns c ON c.tenant_id=r.tenant_id AND c.id=r.campaign_id
 WHERE r.tenant_id=tenant_id AND r.status='queued'
  AND r.next_attempt_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000)
  AND c.status IN('scheduled','running') AND c.scheduled_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000));

-- name: PlatformClaimMarketingRecipient :one
-- scope: platform

UPDATE marketing_recipients SET status='sending', attempts=attempts+1, last_turn=(SELECT last_turn  WHERE next_attempt_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000) AND EXISTS(SELECT 1 FROM marketing_campaigns c WHERE c.tenant_id=tenant_id AND c.id=campaign_id AND c.status IN('scheduled','running') AND c.scheduled_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000)) AND EXISTS(SELECT 1 FROM platform_marketing_dispatch d WHERE d.tenant_id=tenant_id)
WHERE status='queued' AND EXISTS(SELECT 1 FROM platform_marketing_dispatch d WHERE d.tenant_id=tenant_id)
ORDER BY id LIMIT 1
RETURNING tenant_id, id);
-- name: PlatformAuthorizeMarketingSend :one
-- scope: platform

SELECT urmail_authorize_marketing_send(@tenant_id,@recipient_id,@lease_owner,@fence) AS result;

-- name: PlatformFinishMarketingSend :one
-- scope: platform

SELECT urmail_finish_marketing_send(@tenant_id,@recipient_id,@lease_owner,@fence,@result) AS finished;

-- name: PlatformReleaseMarketingClaim :execrows
-- scope: platform

UPDATE marketing_recipients SET status='queued',lease_owner=NULL,lease_until=NULL,next_attempt_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)+30000000
WHERE tenant_id=?1 AND id=?2 AND lease_owner=?3 AND fence=?4 AND status='leased';

-- name: PlatformGetMarketingDelivery :one
-- scope: platform

SELECT r.id,r.tenant_id,r.email,r.campaign_id,c.subject_snapshot,c.body_snapshot,ch.kind,ch.encrypted_config
FROM marketing_recipients r JOIN marketing_campaigns c ON c.tenant_id=r.tenant_id AND c.id=r.campaign_id
JOIN marketing_channels ch ON ch.tenant_id=c.tenant_id AND ch.id=c.channel_id
WHERE r.tenant_id=?1 AND r.id=?2;

-- name: PlatformCompleteMarketingCampaigns :execrows
-- scope: platform

UPDATE marketing_campaigns SET status='completed' WHERE status IN('scheduled','running')
AND NOT EXISTS(SELECT 1  WHERE EXISTS(SELECT 1 FROM marketing_recipients r WHERE r.campaign_id=id AND r.status IN('queued','leased','submitting','uncertain')));
-- name: PlatformResolveMarketingAttempt :one
-- scope: platform

SELECT urmail_resolve_marketing_attempt(@tenant_id,@recipient_id,@outcome,@reason) AS resolved;

-- name: PlatformRecordMarketingFeedback :one
-- scope: platform

SELECT urmail_record_marketing_feedback(@channel_id,@event_id,@recipient_id,@status,@payload_hash) AS result;

-- name: PlatformUpdateMarketingChannel :one
-- scope: platform

UPDATE marketing_channels SET name=?3,kind=?4,sender_address=?5,encrypted_config=?6,enabled=?7 WHERE tenant_id=?1 AND id=?2 RETURNING id,tenant_id,name,kind,enabled,created_at;

-- name: PlatformMarkMarketingRecipientRevoked :one
-- scope: platform

SELECT urmail_mark_marketing_recipient_revoked(@tenant_id,@recipient_id,@lease_owner,@fence) AS revoked;

-- name: PlatformSkipMarketingRecipient :one
-- scope: platform

SELECT urmail_skip_marketing_recipient(@tenant_id,@recipient_id,@lease_owner,@fence,@reason) AS skipped;

-- name: PlatformMarketingAttemptStillAllowed :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM marketing_recipients r JOIN marketing_campaigns c ON c.tenant_id=r.tenant_id AND c.id=r.campaign_id
 JOIN marketing_channels ch ON ch.tenant_id=c.tenant_id AND ch.id=c.channel_id
 WHERE r.tenant_id = @tenant_id AND r.id = @recipient_id AND r.lease_owner = @lease_owner AND r.fence = @fence
 AND r.status='submitting' AND r.lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND c.status IN('scheduled','running') AND ch.enabled AND EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=r.tenant_id AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready')))
 AND NOT EXISTS(SELECT 1 FROM marketing_suppressions s WHERE s.tenant_id=r.tenant_id AND s.email=r.email)
 AND EXISTS(SELECT 1 FROM marketing_contacts mc WHERE mc.tenant_id=r.tenant_id AND mc.list_id=c.list_id AND mc.email=r.email AND mc.opted_in)
 AND EXISTS(SELECT 1 FROM mailboxes m JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE m.tenant_id=r.tenant_id AND m.address=ch.sender_address AND m.status='active' AND m.send_enabled AND d.status='active')) AS allowed;
