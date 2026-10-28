-- name: SupportDiagnosticMailboxCounts :one
SELECT count(*)::bigint AS total,
 count(*) FILTER (WHERE m.status='active')::bigint AS active,
 count(*) FILTER (WHERE m.status IN('disabled','locked'))::bigint AS suspended,
 count(*) FILTER (WHERE m.quota_bytes>0 AND coalesce(u.bytes_used,0)>=m.quota_bytes)::bigint AS over_quota
FROM mailboxes m
LEFT JOIN LATERAL (
 SELECT usage.bytes_used FROM mailbox_usage usage WHERE usage.tenant_id=sqlc.arg('tenant_id')::bigint AND usage.mailbox_id=m.id ORDER BY usage.day DESC LIMIT 1
) u ON true WHERE m.tenant_id=sqlc.arg('tenant_id')::bigint;
