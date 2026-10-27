-- name: PlatformEnqueueDueRenewals :exec
INSERT INTO subscription_renewal_attempts(tenant_id,subscription_id,period_end,state,next_attempt_at)
SELECT s.tenant_id,s.id,s.period_end,'pending',s.period_end
FROM subscriptions s
WHERE s.status IN ('active','past_due') AND s.plan_id<>'free' AND s.cancel_at_period_end=false
 AND s.period_end<=now()+interval '3 days' AND s.period_end>now()-interval '7 days'
ON CONFLICT (subscription_id, period_end) DO NOTHING;

-- name: PlatformClaimRenewalAttempt :one
-- BILL-004: only an attempt whose recorded period_end is still the
-- subscription's current billable cycle may be claimed. A settled order or a
-- completed renewal advances (or cancels) the cycle, so a stale attempt is
-- never claimed again and can therefore never retry or notify the tenant.
WITH candidate AS (
 SELECT a.id FROM subscription_renewal_attempts a
 JOIN subscriptions s ON s.id=a.subscription_id
 WHERE a.state='pending' AND a.next_attempt_at<=now()
  AND s.status IN ('active','past_due') AND s.period_end=a.period_end
 ORDER BY a.next_attempt_at,a.id FOR UPDATE OF a SKIP LOCKED LIMIT 1
)
UPDATE subscription_renewal_attempts a SET attempts=a.attempts+1,updated_at=now()
FROM candidate c WHERE a.id=c.id RETURNING a.*;

-- name: PlatformCancelSupersededRenewalAttempts :exec
-- BILL-004: a settled order supersedes the old billing cycle. Pending
-- attempts whose period is no longer the subscription's current cycle, or
-- whose subscription is no longer billable, are canceled with an explicit
-- reason so they can never be retried or notify the tenant again.
UPDATE subscription_renewal_attempts a
SET state='canceled', last_error='SUPERSEDED_BY_PAYMENT', updated_at=now()
FROM subscriptions s
WHERE s.tenant_id=$1 AND a.subscription_id=s.id AND a.state='pending'
 AND (s.status NOT IN ('active','past_due') OR s.period_end IS DISTINCT FROM a.period_end);

-- name: PlatformFailRenewalAttempt :exec
-- Failed attempts retry with exponential backoff (1, 2, 4, 8, 16 days) and
-- cap at 5 recorded attempts, after which the attempt stays failed.
UPDATE subscription_renewal_attempts
SET state=CASE WHEN attempts>=5 THEN 'failed' ELSE 'pending' END,
 next_attempt_at=now()+make_interval(days => (1 << LEAST(attempts-1, 4))::int),
 last_error=$2, updated_at=now()
WHERE id=$1 AND state='pending';

-- name: PlatformMarkRenewalAttemptUncertain :exec
-- An uncertain provider result must never be resolved by charging again
-- blindly: keep the attempt retryable, mark the explicit reason, and delay.
-- The provider-side idempotency key (dedupe_key) makes the retry safe.
UPDATE subscription_renewal_attempts
SET state='pending', last_error='PROVIDER_UNCERTAIN',
 next_attempt_at=now()+interval '6 hours', updated_at=now()
WHERE id=$1 AND tenant_id=$2 AND state='pending';

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
 period_start=GREATEST(s.period_end, now()),
 period_end=GREATEST(s.period_end, now())+CASE s.billing_cycle
  WHEN 'monthly' THEN interval '1 month'
  WHEN 'quarterly' THEN interval '3 months'
  ELSE interval '1 year' END,
 status=CASE WHEN s.status IN ('active','past_due') THEN 'active' ELSE s.status END
WHERE s.id=$1 AND s.tenant_id=$2 AND s.status IN ('active','past_due')
RETURNING s.period_end, s.status;

-- name: PlatformMarkRenewalAttemptSucceeded :exec
UPDATE subscription_renewal_attempts SET state='succeeded', last_error='', updated_at=now()
WHERE id=$1 AND tenant_id=$2 AND state IN ('pending','failed');

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
