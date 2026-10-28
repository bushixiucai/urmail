-- name: PlatformEnqueueDueRenewals :exec
INSERT INTO subscription_renewal_attempts(tenant_id,subscription_id,period_end,state,next_attempt_at,enrollment_id,binding_id,merchant_id,merchant_scope,amount_cents,currency,plan_snapshot,plan_snapshot_digest,billing_cycle,renewal_period_end,calendar_snapshot)
SELECT s.tenant_id,s.id,s.period_end,'pending',s.period_end,e.id,b.id,m.id,m.provider||':'||m.environment||':'||m.account_id,e.amount_cents,e.currency,e.plan_snapshot,e.plan_snapshot_digest,e.billing_cycle,e.renewal_period_end,e.calendar_snapshot
FROM subscriptions s
JOIN subscription_renewal_enrollments e ON e.tenant_id=s.tenant_id AND e.subscription_id=s.id AND e.state='active' AND e.expected_period_end=s.period_end
JOIN subscription_renewal_authorizations b ON b.tenant_id=e.tenant_id AND b.id=e.binding_id AND b.subscription_id=s.id AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>now())
JOIN payment_merchants m ON m.id=b.merchant_id AND m.enabled
JOIN payment_recurring_capabilities c ON c.merchant_id=m.id AND c.merchant_approved AND c.runtime_available AND c.evidence_at IS NOT NULL AND c.evidence_expires_at>now() AND c.merchant_config_at=m.updated_at AND b.merchant_config_at=m.updated_at
WHERE s.status IN('active','past_due') AND s.plan_id<>'free' AND s.entitlement_source='standard' AND NOT s.cancel_at_period_end
 AND s.plan_id=e.plan_id AND s.billing_cycle=e.billing_cycle AND e.currency=ANY(m.currencies)
 AND s.period_end<=now()+interval '3 days' AND s.period_end>now()-interval '7 days'
ON CONFLICT(subscription_id,period_end) DO UPDATE SET enrollment_id=EXCLUDED.enrollment_id,binding_id=EXCLUDED.binding_id,merchant_id=EXCLUDED.merchant_id,merchant_scope=EXCLUDED.merchant_scope,amount_cents=EXCLUDED.amount_cents,currency=EXCLUDED.currency,plan_snapshot=EXCLUDED.plan_snapshot,plan_snapshot_digest=EXCLUDED.plan_snapshot_digest,billing_cycle=EXCLUDED.billing_cycle,renewal_period_end=EXCLUDED.renewal_period_end,calendar_snapshot=EXCLUDED.calendar_snapshot,state='pending',next_attempt_at=EXCLUDED.next_attempt_at,last_error='',claim_token=subscription_renewal_attempts.claim_token+1
WHERE subscription_renewal_attempts.state='canceled' AND subscription_renewal_attempts.side_effect_intent_at IS NULL AND subscription_renewal_attempts.enrollment_id IS DISTINCT FROM EXCLUDED.enrollment_id;

-- name: PlatformRecoverRenewalLeases :exec
UPDATE subscription_renewal_attempts SET state=CASE WHEN side_effect_intent_at IS NULL THEN 'pending' ELSE 'uncertain' END,lease_until=NULL,next_attempt_at=now(),updated_at=now()
WHERE state IN('preparing','charging','reconciling') AND lease_until<now();

-- name: PlatformClaimRenewalAttempt :one
WITH candidate AS (
 SELECT a.id FROM subscription_renewal_attempts a
 JOIN subscriptions s ON s.id=a.subscription_id AND s.tenant_id=a.tenant_id
 WHERE a.next_attempt_at<=now() AND position('RESTORE_RECONCILIATION_REQUIRED' IN COALESCE(a.last_error,''))=0 AND (
 (a.state='pending' AND s.status IN('active','past_due') AND s.period_end=a.period_end)
 OR a.state='uncertain')
 ORDER BY a.next_attempt_at,a.id FOR UPDATE OF a SKIP LOCKED LIMIT 1
)
UPDATE subscription_renewal_attempts a SET state=CASE WHEN a.side_effect_intent_at IS NULL THEN 'preparing' ELSE 'reconciling' END,
 claim_token=a.claim_token+1,lease_until=now()+interval '2 minutes',attempts=a.attempts+1,updated_at=now()
FROM candidate c WHERE a.id=c.id RETURNING a.*;

-- name: PlatformBeginRenewalCharge :one
UPDATE subscription_renewal_attempts a SET state='charging',charge_count=a.charge_count+1,side_effect_intent_at=now(),dedupe_key=$4,updated_at=now()
FROM subscriptions s,subscription_renewal_enrollments e,subscription_renewal_authorizations b,payment_merchants m,payment_recurring_capabilities c
WHERE a.id=$1 AND a.tenant_id=$2 AND a.claim_token=$3 AND a.state='preparing' AND a.lease_until>now() AND a.side_effect_intent_at IS NULL AND a.charge_count<5
 AND s.id=a.subscription_id AND s.tenant_id=a.tenant_id AND s.period_end=a.period_end AND s.status IN('active','past_due') AND NOT s.cancel_at_period_end
 AND e.id=a.enrollment_id AND e.tenant_id=a.tenant_id AND e.state='active' AND e.expected_period_end=a.period_end
 AND e.amount_cents=a.amount_cents AND e.currency=a.currency AND e.plan_snapshot_digest=a.plan_snapshot_digest
 AND b.id=a.binding_id AND b.tenant_id=a.tenant_id AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>now())
 AND m.id=a.merchant_id AND m.enabled AND b.merchant_config_at=m.updated_at
 AND c.merchant_id=m.id AND c.merchant_approved AND c.runtime_available AND c.evidence_expires_at>now() AND c.merchant_config_at=m.updated_at
RETURNING a.*;

-- name: PlatformCompleteRenewalClaim :execrows
UPDATE subscription_renewal_attempts SET
 state=CASE WHEN $4::text='failed' AND charge_count<5 THEN 'pending' ELSE $4::text END,
 last_error=$5,lease_until=NULL,
 next_attempt_at=CASE WHEN $4::text='failed' THEN now()+make_interval(days => (1 << LEAST(GREATEST(charge_count-1,0),4))::int) ELSE now()+interval '6 hours' END,
 charge_history=CASE WHEN $4::text='failed' THEN charge_history||jsonb_build_array(jsonb_build_object('key',dedupe_key,'intent_at',side_effect_intent_at,'outcome','declined','observed_at',now())) ELSE charge_history END,
 side_effect_intent_at=CASE WHEN $4::text='failed' AND charge_count<5 THEN NULL ELSE side_effect_intent_at END,
 dedupe_key=CASE WHEN $4::text='failed' AND charge_count<5 THEN '' ELSE dedupe_key END,
 updated_at=now()
WHERE id=$1 AND tenant_id=$2 AND claim_token=$3 AND state IN('preparing','charging','reconciling');

-- name: PlatformCancelSupersededRenewalAttempts :exec
-- BILL-004: a settled order supersedes the old billing cycle. Pending
-- attempts whose period is no longer the subscription's current cycle, or
-- whose subscription is no longer billable, are canceled with an explicit
-- reason so they can never be retried or notify the tenant again.
UPDATE subscription_renewal_attempts a
SET state='canceled', last_error='SUPERSEDED_BY_PAYMENT', updated_at=now()
FROM subscriptions s
WHERE s.tenant_id=$1 AND a.subscription_id=s.id AND a.state IN('pending','preparing') AND a.side_effect_intent_at IS NULL
 AND (s.status NOT IN ('active','past_due') OR s.period_end IS DISTINCT FROM a.period_end);

-- name: PlatformFailRenewalAttempt :exec
-- Failed attempts retry with exponential backoff (1, 2, 4, 8, 16 days) and
-- cap at 5 recorded attempts, after which the attempt stays failed.
UPDATE subscription_renewal_attempts
SET state=CASE WHEN attempts>=5 THEN 'failed' ELSE 'pending' END,
 next_attempt_at=now()+make_interval(days => (1 << LEAST(attempts-1, 4))::int),
 last_error=$2, updated_at=now()
WHERE id=$1 AND state IN('pending','preparing') AND side_effect_intent_at IS NULL;

-- name: PlatformMarkRenewalAttemptUncertain :exec
-- An uncertain provider result permits read-only reconciliation only.
-- Retain intent even when this compatibility operation observes a lost reply.
UPDATE subscription_renewal_attempts
SET state='uncertain', side_effect_intent_at=COALESCE(side_effect_intent_at,now()),lease_until=NULL,last_error='PROVIDER_UNCERTAIN',
 next_attempt_at=now()+interval '6 hours', updated_at=now()
WHERE id=$1 AND tenant_id=$2 AND state IN('preparing','charging','reconciling');

-- name: PlatformLockRenewalAttempt :one
SELECT * FROM subscription_renewal_attempts WHERE id=$1 AND tenant_id=$2 FOR UPDATE;

-- name: PlatformSetRenewalDedupeKey :exec
UPDATE subscription_renewal_attempts SET dedupe_key=$2, updated_at=now()
WHERE id=$1 AND tenant_id=$3 AND dedupe_key='';

-- name: PlatformInsertRenewalPayment :one
INSERT INTO payments(tenant_id,provider,account_scope,provider_txn_id,amount_cents,currency,status,raw,paid_at)
VALUES ($1,$2,$3,$4,$5,$6,'succeeded',$7,now())
ON CONFLICT DO NOTHING
RETURNING *;

-- name: PlatformGetRenewalPayment :one
SELECT * FROM payments WHERE provider=$1 AND account_scope=$2 AND provider_txn_id=$3;

-- name: PlatformExtendSubscriptionForRenewal :one
-- Monotonic extension only: the new period starts at the old period_end, or
-- now when the lapsed period ended further in the past, and billing state is
-- restored only from past_due/active. Independent suspensions, revocations
-- and legal holds are intentionally untouched.
UPDATE subscriptions s SET
 period_start=CASE WHEN sqlc.narg(renewal_period_end)::timestamptz IS NULL THEN GREATEST(s.period_end,now()) ELSE s.period_end END,
 period_end=COALESCE(sqlc.narg(renewal_period_end)::timestamptz,GREATEST(s.period_end, now())+CASE s.billing_cycle
  WHEN 'monthly' THEN interval '1 month'
  WHEN 'quarterly' THEN interval '3 months'
  ELSE interval '1 year' END),
 status=CASE WHEN s.status IN ('active','past_due') THEN 'active' ELSE s.status END
WHERE s.id=$1 AND s.tenant_id=$2 AND s.status IN ('active','past_due') AND s.period_end=sqlc.arg(expected_period_end)
RETURNING s.period_end, s.status;

-- name: PlatformMarkRenewalAttemptSucceeded :exec
UPDATE subscription_renewal_attempts SET state='succeeded', last_error='', updated_at=now()
WHERE id=$1 AND tenant_id=$2 AND state IN ('pending','preparing','charging','uncertain','reconciling','failed');

-- name: PlatformFindRenewalAttemptForPayment :one
SELECT a.* FROM subscription_renewal_attempts a
JOIN payments p ON (p.raw->>'renewal_attempt_id')::bigint=a.id
WHERE p.provider=$1 AND p.account_scope=$2 AND p.provider_txn_id=$3
LIMIT 1;

-- name: PlatformFindRenewalAttemptByDedupeKey :one
SELECT * FROM subscription_renewal_attempts WHERE dedupe_key=$1;

-- name: PlatformGetRenewalChargeContext :one
SELECT s.tenant_id, s.id AS subscription_id, s.plan_id, s.period_end, s.billing_cycle,
 p.price_cents, p.currency
FROM subscriptions s JOIN plans p ON p.code=s.plan_id
WHERE s.id=$1;

-- name: PlatformEnqueueRenewalFailure :exec
INSERT INTO notification_outbox(tenant_id,kind,message_key,params,dedupe_key,channel,state)
SELECT a.tenant_id,'renewal_failed','notification.renewal_failed',jsonb_build_object('attempts',a.attempts,'period_end',a.period_end),
 'renewal-failed:'||a.id||':'||a.attempts,'in_app','delivered'
FROM subscription_renewal_attempts a WHERE a.id=$1
ON CONFLICT(dedupe_key) DO NOTHING;

-- name: PlatformAdvanceRenewalEnrollment :exec
UPDATE subscription_renewal_enrollments SET expected_period_end=$3,renewal_period_end=$4,updated_at=now() WHERE id=$1 AND tenant_id=$2 AND state='active';
