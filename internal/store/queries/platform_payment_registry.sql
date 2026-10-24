-- name: PlatformListPaymentMerchants :many
SELECT id,provider,environment,account_id,name,enabled,currencies,entrypoint,checkout_limits,created_at,updated_at FROM payment_merchants ORDER BY id;

-- name: PlatformGetPaymentMerchant :one
SELECT * FROM payment_merchants WHERE id=$1;

-- name: PlatformCreatePaymentMerchant :one
INSERT INTO payment_merchants(provider,environment,account_id,name,enabled,currencies,entrypoint,encrypted_config,checkout_limits) VALUES($1,$2,$3,$4,$5,$6,$7,$8,COALESCE(sqlc.narg(checkout_limits)::jsonb,'{}'::jsonb)) RETURNING id,provider,environment,account_id,name,enabled,currencies,entrypoint,checkout_limits,created_at,updated_at;

-- name: PlatformUpdatePaymentMerchant :one
UPDATE payment_merchants SET name=$2,enabled=$3,currencies=$4,entrypoint=$5,encrypted_config=$6,checkout_limits=COALESCE(sqlc.narg(checkout_limits)::jsonb,'{}'::jsonb),updated_at=now() WHERE id=$1 RETURNING id,provider,environment,account_id,name,enabled,currencies,entrypoint,checkout_limits,created_at,updated_at;

-- name: PlatformRecordMerchantWebhook :one
INSERT INTO payment_webhook_inbox(merchant_id,event_id,payload_hash,payload) VALUES($1,$2,$3,$4) ON CONFLICT(merchant_id,event_id) DO UPDATE SET updated_at=payment_webhook_inbox.updated_at RETURNING *;

-- name: PlatformClaimMerchantWebhook :one
UPDATE payment_webhook_inbox SET state='processing',processing_until=now()+interval '2 minutes',updated_at=now()
WHERE merchant_id=$1 AND event_id=$2 AND payload_hash=$3 AND state='received' RETURNING *;

-- name: PlatformFinishMerchantWebhook :execrows
UPDATE payment_webhook_inbox SET state=$3,result_code=$4,updated_at=now(),processing_until=NULL WHERE merchant_id=$1 AND event_id=$2 AND state='processing';

-- name: PlatformExpireMerchantWebhookClaims :execrows
UPDATE payment_webhook_inbox SET state='uncertain',result_code='processing_lease_expired',updated_at=now() WHERE state='processing' AND processing_until<now();

-- name: PlatformSaveMerchantCheckout :execrows
UPDATE payment_checkout_intents i SET session_id=$3,checkout_url=$4,qr_code=$5,
 state=CASE WHEN i.state<>'cancel_requested' AND EXISTS(SELECT 1 FROM orders o WHERE o.tenant_id=i.tenant_id AND o.id=i.order_id AND o.status='pending') THEN 'ready' ELSE 'cancel_requested' END,updated_at=now()
WHERE i.tenant_id=$1 AND i.order_id=$2 AND i.state IN('creating','uncertain','cancel_requested');

-- name: PlatformGetMerchantOrderByReference :one
SELECT * FROM orders WHERE (@order_id::bigint>0 AND id = @order_id::bigint) OR (@order_id::bigint=0 AND order_no = @order_no::text);

-- name: PlatformFindPaymentMerchantByScope :one
SELECT * FROM payment_merchants WHERE provider=$1 AND provider||':'||environment||':'||account_id=$2;

-- name: PlatformListMerchantCheckoutsToRecover :many
SELECT i.* FROM payment_checkout_intents i WHERE (i.account_scope<>'legacy' OR i.provider='stripe') AND i.state IN('creating','uncertain','cancel_requested','ready') AND (i.state<>'ready' OR EXISTS(SELECT 1 FROM orders o WHERE o.id=i.order_id AND o.status='pending')) AND i.updated_at<now()-interval '2 minutes' ORDER BY i.updated_at,i.order_id LIMIT 100;

-- name: PlatformTouchMerchantCheckout :exec
UPDATE payment_checkout_intents SET updated_at=now() WHERE tenant_id=$1 AND order_id=$2;

-- name: PlatformFlagCheckoutReview :exec
INSERT INTO operational_alerts(alert_key,tenant_id,kind,state) VALUES('payment-checkout:'||sqlc.arg(order_id)::bigint,sqlc.arg(tenant_id)::bigint,'payment_checkout_review','firing')
ON CONFLICT(alert_key) DO UPDATE SET state='firing',last_seen_at=now(),resolved_at=NULL;

-- name: PlatformResolveCheckoutReview :exec
UPDATE operational_alerts SET state='resolved',last_seen_at=now(),resolved_at=now() WHERE alert_key='payment-checkout:'||sqlc.arg(order_id)::bigint;

-- name: PlatformGetMerchantOrderBySession :one
SELECT o.* FROM orders o JOIN payment_checkout_intents i ON i.tenant_id=o.tenant_id AND i.order_id=o.id WHERE i.provider=sqlc.arg(provider)::text AND i.account_scope=sqlc.arg(account_scope)::text AND i.session_id=sqlc.arg(session_id)::text;

-- name: PlatformLockPaymentMerchant :one
SELECT * FROM payment_merchants WHERE id=$1 FOR UPDATE;
