-- name: PlatformFinancialOrderFacts :many
SELECT jsonb_build_object(
 'restore_epoch',urmail_restore_sync_epoch(),
 'merchant_state',(SELECT jsonb_build_object('id',m.id,'enabled',m.enabled,'version_at',m.updated_at,'provider',m.provider,'environment',m.environment,'currencies',m.currencies) FROM payment_merchants m JOIN payment_checkout_intents c ON c.provider=m.provider AND c.account_scope=m.provider||':'||m.environment||':'||m.account_id WHERE c.tenant_id=o.tenant_id AND c.order_id=o.id),
 'id',o.id,'tenant_id',o.tenant_id,'order_no',o.order_no,'kind',o.kind,'status',o.status,
 'amount_cents_exact',o.amount_cents::text,'currency',o.currency,'billing_cycle',o.billing_cycle,
 'created_at',o.created_at,'paid_at',o.paid_at,'quote_digest',o.quote_digest,
 'items',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',i.id,'kind',i.kind,'plan_code',i.plan_code,'quantity',i.quantity,'unit_price_cents_exact',i.unit_price_cents::text,'amount_cents_exact',i.amount_cents::text,'frozen',i.meta) ORDER BY i.id) FROM order_items i WHERE i.tenant_id=o.tenant_id AND i.order_id=o.id),'[]'::jsonb),
 'payments',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',p.id,'provider',p.provider,'account_scope',p.account_scope,'reference',p.provider_txn_id,'amount_cents_exact',p.amount_cents::text,'currency',p.currency,'status',p.status,'paid_at',p.paid_at) ORDER BY p.id) FROM payments p WHERE p.tenant_id=o.tenant_id AND p.order_id=o.id),'[]'::jsonb),
 'checkout', (SELECT jsonb_build_object('provider',c.provider,'account_scope',c.account_scope,'state',c.state,'restore_hold',c.restore_hold,'updated_at',c.updated_at) FROM payment_checkout_intents c WHERE c.tenant_id=o.tenant_id AND c.order_id=o.id),
 'refunds',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',r.id,'refund_no',r.refund_no,'payment_id',r.payment_id,'amount_cents_exact',r.amount_cents::text,'requested_amount_cents_exact',r.requested_amount_cents::text,'currency',r.currency,'status',r.status,'entitlement_action',r.entitlement_action,'entitlement_applied_at',r.entitlement_applied_at,'target_term_order_id',r.target_term_order_id,'completed_at',r.completed_at,'last_error_code',r.last_error_code) ORDER BY r.id) FROM refunds r WHERE r.tenant_id=o.tenant_id AND r.order_id=o.id),'[]'::jsonb),
 'reviews',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM payment_manual_reviews r WHERE r.tenant_id=o.tenant_id AND r.order_id=o.id),'[]'::jsonb),
 'terms',COALESCE((SELECT jsonb_agg(jsonb_build_object('order_id',s.order_id,'basis_order_id',s.basis_order_id,'period_start',s.period_start,'billing_period_start',s.billing_period_start,'period_end',s.period_end,'base_amount_cents_exact',s.base_amount_cents::text,'currency',s.currency,'billing_cycle',s.billing_cycle,'plan_snapshot',s.plan_snapshot,'calendar_snapshot',s.calendar_snapshot,'revoked_at',s.revoked_at,'revoked_by_refund',s.revoked_by_refund) ORDER BY s.order_id) FROM subscription_terms s WHERE s.tenant_id=o.tenant_id AND s.order_id=o.id),'[]'::jsonb),
 'subscription', (SELECT jsonb_build_object('id',s.id,'status',s.status,'plan_id',s.plan_id,'period_start',s.period_start,'period_end',s.period_end,'cancel_at_period_end',s.cancel_at_period_end) FROM subscriptions s WHERE s.tenant_id=o.tenant_id ORDER BY s.id DESC LIMIT 1)
)::jsonb AS facts
FROM orders o WHERE o.tenant_id=sqlc.arg(tenant_id)
AND ((sqlc.arg(order_id)::bigint>0 AND o.id=sqlc.arg(order_id)) OR
 (sqlc.arg(order_id)::bigint=0 AND o.created_at>=sqlc.arg(from_at)::timestamptz AND o.created_at<sqlc.arg(to_at)::timestamptz AND o.id>sqlc.arg(after_id)::bigint))
ORDER BY o.id LIMIT sqlc.arg(row_limit);

-- name: PlatformFinancialPreviewValid :one
SELECT EXISTS(SELECT 1 FROM audit_logs a WHERE a.tenant_id=$1 AND a.actor_user_id=$2
 AND a.action='PREVIEW receipt.review' AND a.target_type='payment-reviews' AND a.target_id=$3
 AND a.created_at>clock_timestamp()-interval '10 minutes'
 AND a.detail->>'digest'=sqlc.arg(digest)::text AND a.detail->>'facts_sha256'=sqlc.arg(facts_sha256)::text
 AND a.detail->>'decision'=sqlc.arg(decision)::text AND a.detail->>'reason'=sqlc.arg(reason)::text)::boolean;

-- name: PlatformLockFinancialTenantReader :one
SELECT u.id FROM users u JOIN user_tenant_roles r ON r.user_id=u.id
WHERE r.tenant_id=$1 AND u.id=$2 AND u.status='active' AND r.role IN('owner','admin','billing');

-- name: PlatformLockFinancialReviewMerchants :many
SELECT m.id FROM payment_merchants m JOIN payment_checkout_intents c
ON c.provider=m.provider AND c.account_scope=m.provider||':'||m.environment||':'||m.account_id
WHERE c.tenant_id=$1 AND c.order_id=$2 FOR SHARE OF m;

-- name: PlatformLockFinancialReviewPayments :many
SELECT id FROM payments WHERE tenant_id=$1 AND order_id=$2 ORDER BY id FOR SHARE;

-- name: PlatformLockFinancialReviewRefunds :many
SELECT id FROM refunds WHERE tenant_id=$1 AND order_id=$2 ORDER BY id FOR SHARE;

-- name: PlatformFinancialTenantOrderFacts :many
SELECT jsonb_build_object(
 'restore_epoch',urmail_restore_sync_epoch(),
 'merchant_state',NULL,
 'id',o.id,'tenant_id',o.tenant_id,'order_no',o.order_no,'kind',o.kind,'status',o.status,
 'amount_cents_exact',o.amount_cents::text,'currency',o.currency,'billing_cycle',o.billing_cycle,
 'created_at',o.created_at,'paid_at',o.paid_at,'quote_digest',o.quote_digest,
 'items',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',i.id,'kind',i.kind,'plan_code',i.plan_code,'quantity',i.quantity,'unit_price_cents_exact',i.unit_price_cents::text,'amount_cents_exact',i.amount_cents::text,'frozen',i.meta) ORDER BY i.id) FROM order_items i WHERE i.tenant_id=o.tenant_id AND i.order_id=o.id),'[]'::jsonb),
 'payments',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',p.id,'provider',p.provider,'account_scope',p.account_scope,'reference',p.provider_txn_id,'amount_cents_exact',p.amount_cents::text,'currency',p.currency,'status',p.status,'paid_at',p.paid_at) ORDER BY p.id) FROM payments p WHERE p.tenant_id=o.tenant_id AND p.order_id=o.id),'[]'::jsonb),
 'checkout', (SELECT jsonb_build_object('provider',c.provider,'account_scope',c.account_scope,'state',c.state,'restore_hold',c.restore_hold,'updated_at',c.updated_at) FROM payment_checkout_intents c WHERE c.tenant_id=o.tenant_id AND c.order_id=o.id),
 'refunds',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',r.id,'refund_no',r.refund_no,'payment_id',r.payment_id,'amount_cents_exact',r.amount_cents::text,'requested_amount_cents_exact',r.requested_amount_cents::text,'currency',r.currency,'status',r.status,'entitlement_action',r.entitlement_action,'entitlement_applied_at',r.entitlement_applied_at,'target_term_order_id',r.target_term_order_id,'completed_at',r.completed_at,'last_error_code',r.last_error_code) ORDER BY r.id) FROM refunds r WHERE r.tenant_id=o.tenant_id AND r.order_id=o.id),'[]'::jsonb),
 'reviews',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM payment_manual_reviews r WHERE r.tenant_id=o.tenant_id AND r.order_id=o.id),'[]'::jsonb),
 'terms',COALESCE((SELECT jsonb_agg(jsonb_build_object('order_id',s.order_id,'basis_order_id',s.basis_order_id,'period_start',s.period_start,'billing_period_start',s.billing_period_start,'period_end',s.period_end,'base_amount_cents_exact',s.base_amount_cents::text,'currency',s.currency,'billing_cycle',s.billing_cycle,'plan_snapshot',s.plan_snapshot,'calendar_snapshot',s.calendar_snapshot,'revoked_at',s.revoked_at,'revoked_by_refund',s.revoked_by_refund) ORDER BY s.order_id) FROM subscription_terms s WHERE s.tenant_id=o.tenant_id AND s.order_id=o.id),'[]'::jsonb),
 'subscription', (SELECT jsonb_build_object('id',s.id,'status',s.status,'plan_id',s.plan_id,'period_start',s.period_start,'period_end',s.period_end,'cancel_at_period_end',s.cancel_at_period_end) FROM subscriptions s WHERE s.tenant_id=o.tenant_id ORDER BY s.id DESC LIMIT 1)
)::jsonb AS facts
FROM orders o WHERE o.tenant_id=sqlc.arg(tenant_id)
AND EXISTS(SELECT 1 FROM users u JOIN user_tenant_roles r ON r.user_id=u.id WHERE u.id=nullif(current_setting('urmail.user_id',true),'')::bigint AND u.status='active' AND r.tenant_id=o.tenant_id AND r.role IN('owner','admin','billing'))
AND ((sqlc.arg(order_id)::bigint>0 AND o.id=sqlc.arg(order_id)) OR
 (sqlc.arg(order_id)::bigint=0 AND o.created_at>=sqlc.arg(from_at)::timestamptz AND o.created_at<sqlc.arg(to_at)::timestamptz AND o.id>sqlc.arg(after_id)::bigint))
ORDER BY o.id LIMIT sqlc.arg(row_limit);


-- name: PlatformFinancialReadBudget :exec
SELECT set_config('statement_timeout','5000',true),set_config('lock_timeout','2000',true);

-- name: PlatformCreateFinancialReviewRefund :one
INSERT INTO refunds(tenant_id,order_id,payment_id,refund_no,amount_cents,currency,reason,status)
SELECT p.tenant_id,p.order_id,p.id,sqlc.arg(refund_no),p.amount_cents,p.currency,sqlc.arg(reason),'pending'
FROM payment_manual_reviews r JOIN payments p ON p.tenant_id=r.tenant_id AND p.id=r.payment_id
WHERE r.tenant_id=sqlc.arg(tenant_id) AND r.id=sqlc.arg(review_id) AND r.status='open'
ON CONFLICT(refund_no) DO NOTHING RETURNING *;
