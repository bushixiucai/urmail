-- name: PlatformRecordPaymentManualReview :exec
INSERT INTO payment_manual_reviews(tenant_id,order_id,payment_id,reason)
VALUES($1,$2,$3,$4) ON CONFLICT(payment_id) DO NOTHING;

-- name: PlatformListPaymentManualReviews :many
SELECT id,tenant_id,order_id,payment_id,reason,status,resolution_reason,resolved_by,created_at,resolved_at
FROM payment_manual_reviews WHERE status='open' ORDER BY created_at,id LIMIT 100;

-- name: PlatformGetCheckoutIntent :one
SELECT * FROM payment_checkout_intents WHERE tenant_id=$1 AND order_id=$2;

-- name: PlatformCreateCheckoutIntent :one
INSERT INTO payment_checkout_intents(order_id,tenant_id,provider,account_scope)
VALUES($1,$2,$3,$4) ON CONFLICT(order_id) DO NOTHING
RETURNING *;

-- name: PlatformSaveCheckoutSession :execrows
UPDATE payment_checkout_intents SET session_id=$3,checkout_url=$4,qr_code=$5,state='ready',updated_at=now()
WHERE tenant_id=$1 AND order_id=$2 AND state IN('creating','uncertain','ready');

-- name: PlatformMarkCheckoutUncertain :exec
UPDATE payment_checkout_intents SET state='uncertain',updated_at=now()
WHERE tenant_id=$1 AND order_id=$2 AND state='creating';

-- name: PlatformRequestCheckoutCancel :one
UPDATE payment_checkout_intents SET state='cancel_requested',updated_at=now()
WHERE tenant_id=$1 AND order_id=$2 AND state IN('creating','uncertain','ready','cancel_requested') RETURNING *;

-- name: PlatformMarkCheckoutClosed :exec
UPDATE payment_checkout_intents SET state='closed',updated_at=now()
WHERE tenant_id=$1 AND order_id=$2 AND state='cancel_requested';

-- name: PlatformGetPaymentReview :one
SELECT * FROM payment_manual_reviews WHERE id=$1;

-- name: PlatformGetPaymentReviewForUpdate :one
SELECT * FROM payment_manual_reviews WHERE tenant_id=$1 AND id=$2 FOR UPDATE;

-- name: PlatformRestoreReviewOrderPending :execrows
UPDATE orders SET status='pending' WHERE tenant_id=$1 AND id=$2 AND status IN('pending','canceled','failed');

-- name: PlatformResolvePaymentReviewFulfilled :execrows
UPDATE payment_manual_reviews SET status='fulfilled',resolution_reason=$3,resolved_by=$4,resolved_at=now() WHERE tenant_id=$1 AND id=$2 AND status='open';

-- name: PlatformCreateReviewRefund :one
INSERT INTO refunds(tenant_id,order_id,payment_id,refund_no,amount_cents,currency,reason,status)
SELECT p.tenant_id,p.order_id,p.id,$3,p.amount_cents,p.currency,$4,'pending'
FROM payment_manual_reviews r JOIN payments p ON p.tenant_id=r.tenant_id AND p.id=r.payment_id
WHERE r.tenant_id=$1 AND r.id=$2 AND r.status='open'
ON CONFLICT(refund_no) DO UPDATE SET refund_no=EXCLUDED.refund_no
RETURNING *;

-- name: PlatformResolveRefundedReceiptReviews :exec
UPDATE payment_manual_reviews m SET status='refunded',resolution_reason='confirmed_refund:'||r.refund_no,resolved_by=r.approved_by,resolved_at=now()
FROM refunds r JOIN payments p ON p.id=r.payment_id AND p.tenant_id=r.tenant_id
WHERE r.id=$1 AND r.status='completed' AND m.payment_id=p.id AND m.tenant_id=p.tenant_id AND m.status='open'
AND p.amount_cents<=(SELECT COALESCE(sum(amount_cents),0) FROM refunds WHERE tenant_id=p.tenant_id AND payment_id=p.id AND status='completed');

-- name: PlatformPaymentReviewDetails :many
SELECT r.id,r.tenant_id,r.order_id,r.payment_id,r.reason,r.status,r.created_at,p.amount_cents AS received_amount_cents,p.currency AS received_currency,p.provider,p.account_scope,o.amount_cents AS expected_amount_cents,o.currency AS expected_currency,o.kind AS order_kind,o.status AS order_status
FROM payment_manual_reviews r JOIN payments p ON p.tenant_id=r.tenant_id AND p.id=r.payment_id JOIN orders o ON o.tenant_id=r.tenant_id AND o.id=r.order_id
WHERE r.status='open' ORDER BY r.created_at,r.id LIMIT 100;

-- name: PlatformMarkCheckoutReceived :exec
UPDATE payment_checkout_intents SET state='paid',updated_at=now() WHERE tenant_id=$1 AND order_id=$2;

-- name: PlatformReviewByReceipt :one
SELECT * FROM payment_manual_reviews WHERE tenant_id=$1 AND payment_id=$2;

-- name: PlatformMarkCheckoutRefunded :exec
UPDATE payment_checkout_intents i SET state='refunded',updated_at=now() WHERE i.tenant_id=$1 AND i.order_id=$2
AND NOT EXISTS(SELECT 1 FROM payments p WHERE p.tenant_id=i.tenant_id AND p.order_id=i.order_id AND p.status='succeeded' AND p.amount_cents>(SELECT COALESCE(sum(r.amount_cents),0) FROM refunds r WHERE r.tenant_id=p.tenant_id AND r.payment_id=p.id AND r.status='completed'));
