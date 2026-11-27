-- name: PlatformRecallPreviewSource :one
SELECT a.id,a.tenant_id,a.mailbox_id,a.message_id,a.legal_hold,a.deleted_at,
coalesce(nullif(a.raw_sha256,''),c.original_sha256,'')::text AS original_sha256,
m.address,m.status AS mailbox_status,d.status AS domain_status,t.status AS tenant_status,
EXISTS(SELECT 1 FROM archive_entries sibling WHERE sibling.tenant_id=a.tenant_id
 AND sibling.mailbox_id=a.mailbox_id AND sibling.message_id=a.message_id AND sibling.legal_hold AND sibling.deleted_at IS NULL) AS hold_conflict,
(SELECT coalesce(jsonb_agg(jsonb_build_object('entry_id',s.id,'mailbox_id',coalesce(s.mailbox_id,0),'original_sha256',coalesce(nullif(s.raw_sha256,''),sc.original_sha256,''),'held',s.legal_hold,'deleted',s.deleted_at IS NOT NULL) ORDER BY s.id),'[]'::jsonb)
FROM (SELECT * FROM archive_entries bounded WHERE bounded.tenant_id=a.tenant_id AND bounded.message_id=a.message_id ORDER BY bounded.id LIMIT 101) s
LEFT JOIN archive_content sc ON sc.tenant_id=s.tenant_id AND sc.entry_id=s.id)::jsonb AS sources
FROM archive_entries a JOIN tenants t ON t.id=a.tenant_id
LEFT JOIN archive_content c ON c.tenant_id=a.tenant_id AND c.entry_id=a.id
LEFT JOIN mailboxes m ON m.tenant_id=a.tenant_id AND m.id=a.mailbox_id
LEFT JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE a.tenant_id= @tenant_id AND a.id= @entry_id
AND EXISTS(SELECT 1 FROM user_tenant_roles r JOIN users u ON u.id=r.user_id
 WHERE r.tenant_id=a.tenant_id AND r.user_id= @actor_id AND r.role IN('owner','admin') AND u.status='active');

-- name: PlatformRecallActorAuthorized :one
SELECT EXISTS(SELECT 1 FROM user_tenant_roles r JOIN users u ON u.id=r.user_id JOIN tenants t ON t.id=r.tenant_id
WHERE r.tenant_id=$1 AND r.user_id=$2 AND r.role IN('owner','admin') AND u.status='active' AND t.status='active')::boolean AS allowed;

-- name: PlatformLockRecallAuthority :one
SELECT r.user_id FROM user_tenant_roles r JOIN users u ON u.id=r.user_id JOIN tenants t ON t.id=r.tenant_id
WHERE r.tenant_id=$1 AND r.user_id=$2 AND r.role IN('owner','admin') AND u.status='active' AND t.status='active'
FOR SHARE OF r,u,t;

-- name: PlatformLockRecallMailbox :one
SELECT m.address FROM mailboxes m JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE m.tenant_id=$1 AND m.id=$2 AND m.status='active' AND d.status='active'
FOR SHARE OF m,d;

-- name: PlatformFinishReviewedRecall :one
WITH finished AS (
 UPDATE recall_requests r SET status= @status::text,reason= @reason::text,local_result= @result::jsonb,completed_at=statement_timestamp()
 WHERE r.tenant_id= @tenant_id AND r.id= @recall_id AND r.status='pending' AND r.attempt_epoch= @epoch
 RETURNING r.*
), audited AS (
 INSERT INTO audit_logs(tenant_id,actor_user_id,action,target_type,target_id,detail)
 SELECT tenant_id,coalesce(preview_actor_id,requested_by),'compliance.recall.review_complete','recall',id::text,
 jsonb_build_object('status',status,'scope','local_mailbox','epoch',attempt_epoch,'digest',preview_digest,'selection',selected_copy_keys,'result',local_result)
 FROM finished RETURNING id
)
SELECT finished.* FROM finished CROSS JOIN audited;
