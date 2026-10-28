-- name: PlatformListRecurringCapabilities :many
SELECT m.id AS merchant_id,m.provider,m.environment,m.name,m.enabled,m.updated_at AS merchant_config_at,
 COALESCE(c.adapter_key,'')::text AS adapter_key,COALESCE(c.merchant_approved,false)::boolean AS merchant_approved,
 c.evidence_at,c.evidence_expires_at,COALESCE(c.evidence_kind,'')::text AS evidence_kind,c.merchant_config_at AS evidence_config_at
FROM payment_merchants m LEFT JOIN payment_recurring_capabilities c ON c.merchant_id=m.id ORDER BY m.id;

-- name: PlatformSetRecurringRuntime :exec
UPDATE payment_recurring_capabilities SET runtime_available=$2,updated_at=now() WHERE merchant_id=$1;

-- name: PlatformApproveRecurringCapability :exec
INSERT INTO payment_recurring_capabilities(merchant_id,adapter_key,merchant_approved,runtime_available,evidence_at,evidence_expires_at,evidence_kind,merchant_config_at,evidence_reference)
VALUES($1,$2,true,true,now(),$3,$4,$5,$6)
ON CONFLICT(merchant_id) DO UPDATE SET adapter_key=EXCLUDED.adapter_key,merchant_approved=true,runtime_available=true,evidence_at=now(),evidence_expires_at=EXCLUDED.evidence_expires_at,evidence_kind=EXCLUDED.evidence_kind,merchant_config_at=EXCLUDED.merchant_config_at,evidence_reference=EXCLUDED.evidence_reference,updated_at=now();

-- name: PlatformListRenewalBindings :many
SELECT * FROM subscription_renewal_authorizations WHERE tenant_id=$1 AND subscription_id=$2 AND revoked_at IS NULL ORDER BY id;

-- name: PlatformGetRenewalBinding :one
SELECT * FROM subscription_renewal_authorizations WHERE tenant_id=$1 AND id=$2;

-- name: PlatformInsertRenewalBinding :one
INSERT INTO subscription_renewal_authorizations(tenant_id,subscription_id,merchant_id,authorization_ciphertext,authorization_hash,label,currency,verified_at,expires_at,merchant_config_at)
VALUES($1,$2,$3,$4,$5,$6,$7,now(),$8,$9)
ON CONFLICT(merchant_id,authorization_hash) DO UPDATE SET authorization_ciphertext=EXCLUDED.authorization_ciphertext,label=EXCLUDED.label,currency=EXCLUDED.currency,verified_at=now(),expires_at=EXCLUDED.expires_at,revoked_at=NULL,merchant_config_at=EXCLUDED.merchant_config_at
WHERE subscription_renewal_authorizations.tenant_id=EXCLUDED.tenant_id AND subscription_renewal_authorizations.subscription_id=EXCLUDED.subscription_id
RETURNING *;

-- name: PlatformGetRenewalEnrollment :one
SELECT * FROM subscription_renewal_enrollments WHERE tenant_id=$1 AND subscription_id=$2 ORDER BY id DESC LIMIT 1;

-- name: PlatformRevokeRenewalEnrollment :exec
UPDATE subscription_renewal_enrollments SET state='revoked',version=version+1,revoked_at=now(),updated_at=now() WHERE tenant_id=$1 AND subscription_id=$2 AND state='active';

-- name: PlatformCancelUnstartedRenewals :exec
UPDATE subscription_renewal_attempts SET state='canceled',last_error='ENROLLMENT_REVOKED',lease_until=NULL,updated_at=now() WHERE tenant_id=$1 AND subscription_id=$2 AND state IN('pending','preparing') AND side_effect_intent_at IS NULL;

-- name: PlatformInsertRenewalEnrollment :one
INSERT INTO subscription_renewal_enrollments(tenant_id,subscription_id,binding_id,state,consent_version,consented_by,consent_quote_digest,amount_cents,currency,billing_cycle,plan_id,plan_snapshot,plan_snapshot_digest,expected_period_end,renewal_period_end,calendar_snapshot)
VALUES($1,$2,$3,'active',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING *;

-- name: PlatformListTenantRenewalAttempts :many
SELECT * FROM subscription_renewal_attempts WHERE tenant_id=$1 ORDER BY id DESC LIMIT 20;

-- name: PlatformGetRenewalFrozenTerm :one
SELECT order_id,basis_order_id,period_start,period_end,billing_period_start,base_amount_cents,currency,billing_cycle,plan_snapshot,calendar_snapshot
FROM subscription_terms WHERE tenant_id=$1 AND period_end=$2 AND revoked_at IS NULL ORDER BY period_start DESC,order_id DESC LIMIT 1;

-- name: PlatformCreateRenewalOrder :one
INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code,idempotency_key,billing_cycle,paid_at)
VALUES($1,$2,'renew','paid',$3,$4,$5,$6,$7,now()) RETURNING *;

-- name: PlatformBindRenewalPaymentOrder :exec
UPDATE payments SET order_id=$3 WHERE id=$1 AND tenant_id=$2 AND order_id IS NULL;

-- name: PlatformInvalidateRenewalEnrollments :exec
UPDATE subscription_renewal_enrollments e SET state='needs_reauthorization',version=e.version+1,updated_at=now()
WHERE e.state='active' AND NOT EXISTS(
 SELECT 1 FROM subscription_renewal_authorizations b JOIN payment_merchants m ON m.id=b.merchant_id
 JOIN payment_recurring_capabilities c ON c.merchant_id=m.id
 WHERE b.tenant_id=e.tenant_id AND b.id=e.binding_id AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>now())
 AND m.enabled AND b.merchant_config_at=m.updated_at AND c.merchant_config_at=m.updated_at AND c.merchant_approved AND c.runtime_available AND c.evidence_expires_at>now());

-- name: PlatformCancelIneligibleRenewals :exec
UPDATE subscription_renewal_attempts a SET state='canceled',last_error='AUTHORIZATION_REQUIRED',lease_until=NULL,updated_at=now()
WHERE a.state IN('pending','preparing') AND a.side_effect_intent_at IS NULL AND a.enrollment_id IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM subscription_renewal_enrollments e WHERE e.id=a.enrollment_id AND e.tenant_id=a.tenant_id AND e.state='active');
