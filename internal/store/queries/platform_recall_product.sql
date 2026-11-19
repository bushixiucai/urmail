-- name: PlatformRecallPreviewSource :one
-- scope: platform

SELECT a.id,a.tenant_id,a.mailbox_id,a.message_id,a.legal_hold,a.deleted_at,
coalesce(nullif(a.raw_sha256,''),c.original_sha256,'') AS original_sha256,
m.address,m.status AS mailbox_status,d.status AS domain_status,t.status AS tenant_status,
EXISTS(SELECT 1 FROM archive_entries sibling WHERE sibling.tenant_id=a.tenant_id
 AND sibling.mailbox_id=a.mailbox_id AND sibling.message_id=a.message_id AND sibling.legal_hold AND sibling.deleted_at IS NULL) AS hold_conflict,
(SELECT coalesce(json_group_array(json_object('entry_id',s.id,'mailbox_id',coalesce(s.mailbox_id,0),'original_sha256',coalesce(nullif(s.raw_sha256,''),sc.original_sha256,''),'held',s.legal_hold,'deleted',s.deleted_at IS NOT NULL) ORDER BY s.id),'[]')
FROM (SELECT * FROM archive_entries bounded WHERE bounded.tenant_id=a.tenant_id AND bounded.message_id=a.message_id ORDER BY bounded.id LIMIT 101) s
LEFT JOIN archive_content sc ON sc.tenant_id=s.tenant_id AND sc.entry_id=s.id) AS sources
FROM archive_entries a JOIN tenants t ON t.id=a.tenant_id
LEFT JOIN archive_content c ON c.tenant_id=a.tenant_id AND c.entry_id=a.id
LEFT JOIN mailboxes m ON m.tenant_id=a.tenant_id AND m.id=a.mailbox_id
LEFT JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE a.tenant_id= @tenant_id AND a.id= @entry_id
AND EXISTS(SELECT 1 FROM user_tenant_roles r JOIN users u ON u.id=r.user_id
 WHERE r.tenant_id=a.tenant_id AND r.user_id= @actor_id AND r.role IN('owner','admin') AND u.status='active');

-- name: PlatformRecallActorAuthorized :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM user_tenant_roles r JOIN users u ON u.id=r.user_id JOIN tenants t ON t.id=r.tenant_id
WHERE r.tenant_id=?1 AND r.user_id=?2 AND r.role IN('owner','admin') AND u.status='active' AND t.status='active') AS allowed;

-- name: PlatformLockRecallAuthority :one
-- scope: platform

SELECT r.user_id FROM user_tenant_roles r JOIN users u ON u.id=r.user_id JOIN tenants t ON t.id=r.tenant_id
WHERE r.tenant_id=?1 AND r.user_id=?2 AND r.role IN('owner','admin') AND u.status='active' AND t.status='active';

-- name: PlatformLockRecallMailbox :one
-- scope: platform

SELECT m.address FROM mailboxes m JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE m.tenant_id=?1 AND m.id=?2 AND m.status='active' AND d.status='active';

-- scope: platform

UPDATE recall_requests SET status= @status, reason= @reason, local_result= @result, completed_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id= @tenant_id AND id= @recall_id AND status='pending' AND attempt_epoch= @epoch
RETURNING *;
