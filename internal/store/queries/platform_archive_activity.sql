-- name: PlatformExpireArchiveApprovals :execrows
-- scope: platform

WITH due AS (
 SELECT id,tenant_id FROM archive_approval_requests WHERE case_version>0
 AND state IN('pending','approved') AND expires_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000)
 ORDER BY id LIMIT 100
)
UPDATE archive_approval_requests r SET state='expired',decided_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),decision_reason='APPROVAL_EXPIRED'
FROM due d WHERE r.tenant_id=d.tenant_id AND r.id=d.id;
