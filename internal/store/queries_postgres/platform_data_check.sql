-- name: PlatformDataIssues :many
SELECT 'archive_mailbox_tenant'::text AS issue,c.id::text AS row_id,c.tenant_id,p.tenant_id AS referenced_tenant_id FROM archive_entries c JOIN mailboxes p ON p.id=c.mailbox_id WHERE c.tenant_id<>p.tenant_id
UNION ALL SELECT 'usage_mailbox_tenant',c.mailbox_id::text,c.tenant_id,p.tenant_id FROM mailbox_usage c JOIN mailboxes p ON p.id=c.mailbox_id WHERE c.tenant_id<>p.tenant_id
UNION ALL SELECT 'unsafe_mailbox_identity',m.id::text,m.tenant_id,m.tenant_id FROM mailboxes m JOIN domains d ON d.id=m.domain_id WHERE m.local_part IN('.','..') OR m.local_part ~ '[/\\]' OR m.address<>m.local_part||'@'||d.fqdn
ORDER BY issue,tenant_id,row_id;
