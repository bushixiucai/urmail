-- name: GetTenantStorageUsage :one
SELECT COALESCE(sum(bytes_used),0)::bigint AS bytes_used,
 COALESCE(sum(CASE WHEN quota_bytes=-1 THEN 0 ELSE quota_bytes END) FILTER(WHERE status<>'deleted' OR bytes_used>0 OR usage_measured_at IS NULL),0)::bigint AS bytes_allocated,
 min(usage_measured_at)::timestamptz AS measured_at,
 count(*) FILTER(WHERE usage_measured_at IS NULL OR usage_error IS NOT NULL OR usage_measured_at<now()-interval '10 minutes')::bigint AS unknown_count,
 COALESCE(bool_or(quota_bytes=-1 AND (status<>'deleted' OR bytes_used>0 OR usage_measured_at IS NULL)),false)::boolean AS unlimited
FROM mailboxes WHERE tenant_id=$1;
