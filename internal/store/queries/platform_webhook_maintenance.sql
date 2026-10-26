-- name: PlatformRecordRejectedWebhook :one
SELECT urmail_record_rejected_webhook(sqlc.arg(merchant_id)::bigint,sqlc.arg(reason)::text,sqlc.arg(payload_hash)::text,sqlc.arg(payload_bytes)::integer)::boolean AS recorded;

-- name: PlatformPurgeRejectedWebhookMetadata :execrows
DELETE FROM payment_webhook_rejections WHERE (merchant_id,bucket_start,reason) IN
 (SELECT merchant_id,bucket_start,reason FROM payment_webhook_rejections WHERE bucket_start<now()-interval '7 days' ORDER BY bucket_start LIMIT 1000 FOR UPDATE SKIP LOCKED);

-- name: PlatformCompactLegacyRejectedWebhooks :execrows
UPDATE payment_webhook_inbox i SET payload=jsonb_build_object('redacted',true,'original_bytes',octet_length(i.payload::text)),result_code='verification_rejected_redacted',updated_at=now()
WHERE (i.merchant_id,i.event_id) IN
 (SELECT merchant_id,event_id FROM payment_webhook_inbox
  WHERE state='rejected' AND event_id ~ '^rejected:[a-f0-9]{64}$' AND result_code='verification_rejected'
  ORDER BY created_at LIMIT 50 FOR UPDATE SKIP LOCKED);

-- name: PlatformPurgeLegacyRejectedWebhooks :execrows
DELETE FROM payment_webhook_inbox WHERE (merchant_id,event_id) IN
 (SELECT merchant_id,event_id FROM payment_webhook_inbox
  WHERE state='rejected' AND event_id ~ '^rejected:[a-f0-9]{64}$' AND result_code='verification_rejected_redacted' AND created_at<now()-interval '7 days'
  ORDER BY created_at LIMIT 1000 FOR UPDATE SKIP LOCKED);

-- name: PlatformWebhookMaintenanceStats :one
SELECT
 (SELECT count(*) FROM payment_webhook_rejections)::bigint AS metadata_buckets,
 (SELECT count(*) FROM payment_webhook_rejections WHERE bucket_start<now()-interval '7 days')::bigint AS expired_buckets,
 (SELECT count(*) FROM payment_webhook_inbox WHERE state='rejected' AND event_id ~ '^rejected:[a-f0-9]{64}$' AND result_code='verification_rejected')::bigint AS legacy_bodies,
 (SELECT count(*) FROM payment_webhook_inbox WHERE state='rejected' AND event_id ~ '^rejected:[a-f0-9]{64}$' AND result_code='verification_rejected_redacted' AND created_at<now()-interval '7 days')::bigint AS expired_legacy_rows;
