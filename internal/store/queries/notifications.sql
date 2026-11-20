-- name: ListTenantNotifications :many
-- scope: tenant

-- app-pending: current_setting('urmail.tenant_id'/'urmail.user_id') 会话绑定由
-- S2 装饰器承接（S4 只内联 urmail_archive_notification_visible 的谓词本体）。
SELECT n.id,n.kind,n.message_key,n.params,n.channel,n.state,n.created_at,n.read_at
FROM notification_outbox n
WHERE n.tenant_id=?1 AND (n.user_id IS NULL OR n.user_id=nullif(current_setting('urmail.user_id',true),''))
AND (CASE WHEN n.kind NOT IN('archive_approval','archive_recall') THEN 1 ELSE
 n.tenant_id=nullif(current_setting('urmail.tenant_id',true),'')
 AND nullif(current_setting('urmail.user_id',true),'') IS NOT NULL
 AND CASE n.kind
  WHEN 'archive_approval' THEN EXISTS(SELECT 1 FROM archive_investigations i
   WHERE i.tenant_id=n.tenant_id AND CAST(json_extract(n.params,'$.case_id') AS INTEGER)=i.id
   AND EXISTS(SELECT 1 FROM archive_investigation_members m
    WHERE m.tenant_id=n.tenant_id AND m.investigation_id=i.id
     AND m.user_id=nullif(current_setting('urmail.user_id',true),'') AND m.removed_at IS NULL)
   AND EXISTS(SELECT 1 FROM compliance_grants g JOIN user_tenant_roles r ON r.tenant_id=g.tenant_id AND r.user_id=g.user_id
    WHERE g.tenant_id=n.tenant_id AND g.user_id=nullif(current_setting('urmail.user_id',true),'') AND g.scope='full_content'
     AND NOT g.revoked AND g.expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000)
     AND g.owner_confirmed_at IS NOT NULL AND g.granted_by IS NOT NULL
     AND g.granted_by<>g.user_id AND length(trim(g.reason))>0 AND r.role IN('owner','admin'))
   AND EXISTS(SELECT 1 FROM compliance_grants g JOIN user_tenant_roles r ON r.tenant_id=g.tenant_id AND r.user_id=g.user_id
    WHERE g.tenant_id=n.tenant_id AND g.user_id=nullif(current_setting('urmail.user_id',true),'') AND g.scope='export'
     AND NOT g.revoked AND g.expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000)
     AND g.owner_confirmed_at IS NOT NULL AND g.granted_by IS NOT NULL
     AND g.granted_by<>g.user_id AND length(trim(g.reason))>0 AND r.role IN('owner','admin')))
  WHEN 'archive_recall' THEN EXISTS(SELECT 1 FROM recall_requests rr JOIN user_tenant_roles a ON a.tenant_id=rr.tenant_id AND a.user_id=rr.requested_by
   WHERE rr.tenant_id=n.tenant_id AND rr.requested_by=nullif(current_setting('urmail.user_id',true),'')
    AND a.role IN('owner','admin') AND CAST(json_extract(n.params,'$.recall_id') AS INTEGER)=rr.id)
  ELSE 0 END END)
AND (sqlc.arg(before_id)=0 OR n.id<sqlc.arg(before_id))
ORDER BY n.id DESC LIMIT 50;