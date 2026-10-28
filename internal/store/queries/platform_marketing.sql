-- name: PlatformCreateMarketingChannel :one
INSERT INTO marketing_channels(tenant_id,name,kind,sender_address,encrypted_config,enabled) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,tenant_id,name,kind,enabled,created_at;

-- name: PlatformGetMarketingChannel :one
SELECT * FROM marketing_channels WHERE id=$1;

-- name: PlatformGetActiveMarketingChannel :one
SELECT id,tenant_id,name,kind,encrypted_config FROM marketing_channels
WHERE tenant_id=$1 AND enabled AND kind='dedicated_gateway' ORDER BY id LIMIT 1;

-- name: PlatformRecoverMarketingSubmissions :execrows
UPDATE marketing_recipients SET status='uncertain',result_code='lease_expired_inflight',completed_at=now(),fence=fence+1,lease_until=NULL
WHERE status='submitting' AND lease_until<=now();

-- name: PlatformRecoverMarketingClaims :execrows
UPDATE marketing_recipients SET status='queued',fence=fence+1,lease_owner=NULL,lease_until=NULL
WHERE status='leased' AND lease_until<=now();

-- name: PlatformClaimMarketingRecipient :one
WITH tenant_candidate AS MATERIALIZED (
 SELECT d.tenant_id FROM platform_marketing_dispatch d
 WHERE EXISTS(SELECT 1 FROM marketing_recipients r JOIN marketing_campaigns c ON c.tenant_id=r.tenant_id AND c.id=r.campaign_id
  WHERE r.tenant_id=d.tenant_id AND r.status='queued' AND r.next_attempt_at<=now()
  AND c.status IN('scheduled','running') AND c.scheduled_at<=now())
 ORDER BY d.last_turn,d.tenant_id FOR UPDATE OF d SKIP LOCKED LIMIT 1
), turn AS (
 UPDATE platform_marketing_dispatch d SET last_turn=nextval('platform_marketing_dispatch_turn_seq')
 FROM tenant_candidate t WHERE d.tenant_id=t.tenant_id RETURNING d.tenant_id
), candidate AS (
 SELECT r.id FROM marketing_recipients r JOIN marketing_campaigns c ON c.tenant_id=r.tenant_id AND c.id=r.campaign_id
 JOIN turn d ON d.tenant_id=r.tenant_id
 WHERE r.status='queued' AND r.next_attempt_at<=now() AND c.status IN('scheduled','running') AND c.scheduled_at<=now()
 ORDER BY r.id FOR UPDATE OF r SKIP LOCKED LIMIT 1
)
UPDATE marketing_recipients r SET status='leased',lease_owner= @lease_owner::text,lease_until=now()+interval '2 minutes',fence=r.fence+1
FROM candidate WHERE r.id=candidate.id RETURNING r.*;

-- name: PlatformAuthorizeMarketingSend :one
SELECT urmail_authorize_marketing_send(@tenant_id::bigint,@recipient_id::bigint,@lease_owner::text,@fence::bigint)::text AS result;

-- name: PlatformFinishMarketingSend :one
SELECT urmail_finish_marketing_send(@tenant_id::bigint,@recipient_id::bigint,@lease_owner::text,@fence::bigint,@result::text)::boolean AS finished;

-- name: PlatformReleaseMarketingClaim :execrows
UPDATE marketing_recipients SET status='queued',lease_owner=NULL,lease_until=NULL,next_attempt_at=now()+interval '30 seconds'
WHERE tenant_id=$1 AND id=$2 AND lease_owner=$3 AND fence=$4 AND status='leased';

-- name: PlatformGetMarketingDelivery :one
SELECT r.id,r.tenant_id,r.email,r.campaign_id,c.subject_snapshot,c.body_snapshot,ch.kind,ch.encrypted_config
FROM marketing_recipients r JOIN marketing_campaigns c ON c.tenant_id=r.tenant_id AND c.id=r.campaign_id
JOIN marketing_channels ch ON ch.tenant_id=c.tenant_id AND ch.id=c.channel_id
WHERE r.tenant_id=$1 AND r.id=$2;

-- name: PlatformCompleteMarketingCampaigns :execrows
UPDATE marketing_campaigns c SET status='completed' WHERE c.status IN('scheduled','running')
AND NOT EXISTS(SELECT 1 FROM marketing_recipients r WHERE r.campaign_id=c.id AND r.status IN('queued','leased','submitting','uncertain'));

-- name: PlatformResolveMarketingAttempt :one
SELECT urmail_resolve_marketing_attempt(@tenant_id::bigint,@recipient_id::bigint,@outcome::text,@reason::text)::boolean AS resolved;

-- name: PlatformRecordMarketingFeedback :one
SELECT urmail_record_marketing_feedback(@channel_id::bigint,@event_id::text,@recipient_id::bigint,@status::text,@payload_hash::text)::text AS result;

-- name: PlatformUpdateMarketingChannel :one
UPDATE marketing_channels SET name=$3,kind=$4,sender_address=$5,encrypted_config=$6,enabled=$7 WHERE tenant_id=$1 AND id=$2 RETURNING id,tenant_id,name,kind,enabled,created_at;

-- name: PlatformMarkMarketingRecipientRevoked :one
SELECT urmail_mark_marketing_recipient_revoked(@tenant_id::bigint,@recipient_id::bigint,@lease_owner::text,@fence::bigint)::boolean AS revoked;

-- name: PlatformSkipMarketingRecipient :one
SELECT urmail_skip_marketing_recipient(@tenant_id::bigint,@recipient_id::bigint,@lease_owner::text,@fence::bigint,@reason::text)::boolean AS skipped;

-- name: PlatformMarketingAttemptStillAllowed :one
SELECT EXISTS(SELECT 1 FROM marketing_recipients r JOIN marketing_campaigns c ON c.tenant_id=r.tenant_id AND c.id=r.campaign_id
 JOIN marketing_channels ch ON ch.tenant_id=c.tenant_id AND ch.id=c.channel_id
 WHERE r.tenant_id = @tenant_id::bigint AND r.id = @recipient_id::bigint AND r.lease_owner = @lease_owner::text AND r.fence = @fence::bigint
 AND r.status='submitting' AND r.lease_until>now() AND c.status IN('scheduled','running') AND ch.enabled AND urmail_mail_allowed(r.tenant_id)
 AND NOT EXISTS(SELECT 1 FROM marketing_suppressions s WHERE s.tenant_id=r.tenant_id AND s.email=r.email)
 AND EXISTS(SELECT 1 FROM marketing_contacts mc WHERE mc.tenant_id=r.tenant_id AND mc.list_id=c.list_id AND mc.email=r.email AND mc.opted_in)
 AND EXISTS(SELECT 1 FROM mailboxes m JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE m.tenant_id=r.tenant_id AND m.address=ch.sender_address AND m.status='active' AND m.send_enabled AND d.status='active'))::boolean AS allowed;
