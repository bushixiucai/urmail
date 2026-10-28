-- name: PlatformClaimRecurringWebhook :one
-- Stripe renewal confirmations are reconciled with GET only. No capture or
-- charge operation is permitted when retrying an uncertain receipt.
UPDATE payment_webhook_inbox
SET state='processing',processing_until=now()+interval '2 minutes',updated_at=now()
WHERE merchant_id=$1 AND event_id=$2 AND payload_hash=$3
AND (state IN('received','uncertain') OR (state='processing' AND processing_until<now()))
RETURNING *;

-- name: PlatformFinishRecurringWebhook :execrows
UPDATE payment_webhook_inbox
SET state=$5,result_code=$6,processing_until=NULL,updated_at=now()
WHERE merchant_id=$1 AND event_id=$2 AND payload_hash=$3
AND state='processing' AND processing_until=$4;
