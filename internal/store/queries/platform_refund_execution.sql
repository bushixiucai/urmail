-- name: PlatformListRefundsForApproval :many
SELECT * FROM refunds WHERE ($1::text='' OR status=$1) ORDER BY created_at DESC,id DESC LIMIT 100;

-- name: PlatformGetRefundForUpdate :one
SELECT * FROM refunds WHERE id=$1 FOR UPDATE;

-- name: PlatformApproveRefund :one
UPDATE refunds SET status='approved',amount_cents=sqlc.arg(approved_amount)::bigint,approved_by=sqlc.arg(approved_by),approved_at=now(),approval_reason=sqlc.arg(approval_reason),entitlement_action=sqlc.arg(entitlement_action),entitlement_effective_at=sqlc.narg(entitlement_effective_at),target_term_order_id=sqlc.narg(target_term_order_id)
WHERE id=sqlc.arg(refund_id) AND status='pending' RETURNING *;

-- name: PlatformRejectRefund :one
UPDATE refunds SET status='rejected',approved_by=$2,approved_at=now(),approval_reason=$3 WHERE id=$1 AND status='pending' RETURNING *;

-- name: PlatformClaimApprovedRefund :one
UPDATE refunds r SET status='processing',request_started_at=now(),last_error_code=NULL
FROM (SELECT id FROM refunds WHERE status='approved' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1) candidate
WHERE r.id=candidate.id RETURNING r.*;

-- name: PlatformListRefundsToReconcile :many
SELECT * FROM refunds WHERE status IN('processing','uncertain') AND request_started_at<now()-interval '2 minutes' ORDER BY request_started_at LIMIT 50;

-- name: PlatformRecordRefundProviderState :execrows
UPDATE refunds SET status=$2,provider_refund_no=COALESCE($3,provider_refund_no),completed_at=CASE WHEN $2='completed' THEN now() ELSE completed_at END,last_error_code=$4
WHERE id=$1 AND status IN('processing','uncertain');

-- name: PlatformGetRefundPayment :one
SELECT p.*,o.order_no FROM payments p JOIN orders o ON o.tenant_id=p.tenant_id AND o.id=p.order_id WHERE p.tenant_id=$1 AND p.id=$2;

-- name: PlatformAdjustCommissionForRefund :exec
INSERT INTO commission_refund_adjustments(refund_id,commission_id,amount_cents)
SELECT r.id,c.id,GREATEST(0,LEAST(c.amount_cents-COALESCE((SELECT sum(amount_cents) FROM commission_refund_adjustments WHERE commission_id=c.id),0),
 round(c.amount_cents::numeric*(SELECT sum(amount_cents) FROM refunds WHERE payment_id=r.payment_id AND status='completed')/p.amount_cents)-COALESCE((SELECT sum(amount_cents) FROM commission_refund_adjustments WHERE commission_id=c.id),0)))::bigint
FROM refunds r JOIN payments p ON p.tenant_id=r.tenant_id AND p.id=r.payment_id JOIN commissions c ON c.tenant_id=r.tenant_id AND c.source_payment_id=r.payment_id
WHERE r.id=$1 AND r.status='completed' ON CONFLICT(refund_id,commission_id) DO NOTHING;

-- name: PlatformUpdateRefundedInvoice :exec
UPDATE invoices i SET status='void' WHERE i.source_payment_id=$1 AND i.tenant_id=$2
AND i.amount_cents<=(SELECT COALESCE(sum(amount_cents),0) FROM refunds r WHERE r.payment_id=i.source_payment_id AND r.status='completed');

-- name: PlatformUpdateRefundedOrder :exec
UPDATE orders o SET status=CASE WHEN (SELECT COALESCE(sum(r.amount_cents),0) FROM refunds r JOIN invoices i ON i.tenant_id=r.tenant_id AND i.source_payment_id=r.payment_id WHERE r.order_id=o.id AND r.tenant_id=o.tenant_id AND r.status='completed')>=o.amount_cents THEN 'refunded' ELSE 'partially_refunded' END
WHERE o.tenant_id=$1 AND o.id=$2 AND EXISTS(SELECT 1 FROM invoices i0 WHERE i0.tenant_id=o.tenant_id AND i0.order_id=o.id AND i0.source_payment_id=$3);

-- name: PlatformListRefundEntitlementsDue :many
SELECT * FROM refunds WHERE status='completed' AND entitlement_action<>'none' AND entitlement_effective_at<=now() AND entitlement_applied_at IS NULL ORDER BY id LIMIT 50;

-- name: PlatformRevokeRefundTerm :exec
UPDATE subscription_terms SET revoked_at=now(),revoked_by_refund=$3
WHERE tenant_id=$1 AND (order_id=$2 OR basis_order_id=$2) AND revoked_at IS NULL;

-- name: PlatformRecomputePaidEntitlementEnd :exec
UPDATE subscriptions s SET period_end=GREATEST(s.period_start+interval '1 microsecond',COALESCE(urmail_legacy_current_entitlement_end(s.tenant_id),now()),COALESCE((SELECT max(t.period_end) FROM subscription_terms t WHERE t.tenant_id=s.tenant_id AND t.basis_order_id IS NULL AND t.revoked_at IS NULL),now()))
WHERE s.tenant_id=$1 AND s.entitlement_source='standard' AND s.status IN('active','trialing','past_due');

-- name: PlatformMarkRefundEntitlementApplied :exec
UPDATE refunds SET entitlement_applied_at=now() WHERE id=$1 AND status='completed' AND entitlement_applied_at IS NULL;

-- name: PlatformNextApprovedRefund :one
SELECT * FROM refunds WHERE status='approved' ORDER BY id LIMIT 1;

-- name: PlatformClaimRefundByID :one
UPDATE refunds SET status='processing',request_started_at=now(),last_error_code=NULL WHERE tenant_id=$1 AND id=$2 AND status='approved' RETURNING *;

-- name: PlatformRefundTenant :one
SELECT tenant_id FROM refunds WHERE id=$1;

-- name: PlatformFlagApprovedRefund :exec
UPDATE refunds SET last_error_code=$3 WHERE tenant_id=$1 AND id=$2 AND status='approved';

-- name: PlatformGetRefundTargetTerm :one
SELECT * FROM subscription_terms WHERE tenant_id=$1 AND order_id=$2;

-- name: PlatformPrepareRefundReconciliation :exec
UPDATE refunds SET status='uncertain',request_started_at=now(),last_error_code='manual_provider_query' WHERE id=$1 AND status='approved';

-- name: PlatformReconfirmObservedRefund :execrows
UPDATE refunds SET status='uncertain',approved_by=$2,approved_at=now(),approval_reason=$3,request_started_at=now()
WHERE id=$1 AND approved_by IS NULL AND status IN('pending','uncertain');
