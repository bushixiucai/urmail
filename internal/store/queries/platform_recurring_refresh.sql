-- name: PlatformListRecurringEvidenceRefreshCandidates :many
SELECT c.*, m.provider, m.environment, m.updated_at AS current_merchant_config_at
FROM payment_recurring_capabilities c JOIN payment_merchants m ON m.id=c.merchant_id
WHERE c.merchant_approved AND m.enabled AND c.merchant_config_at=m.updated_at
 AND c.adapter_key=ANY(sqlc.arg(adapter_keys)::text[])
 AND c.evidence_at IS NOT NULL AND c.evidence_expires_at IS NOT NULL
 AND c.evidence_expires_at<=now()+interval '5 minutes'
ORDER BY c.merchant_id;

-- name: PlatformRefreshRecurringEvidence :execrows
UPDATE payment_recurring_capabilities c
SET evidence_at=clock_timestamp(), evidence_expires_at=sqlc.arg(expires_at),
 evidence_reference=sqlc.arg(reference), updated_at=clock_timestamp()
FROM payment_merchants m
WHERE c.merchant_id=sqlc.arg(merchant_id) AND m.id=c.merchant_id
 AND c.merchant_approved AND m.enabled
 AND c.adapter_key=sqlc.arg(adapter_key)
 AND c.updated_at=sqlc.arg(capability_updated_at)
 AND c.evidence_at=sqlc.arg(previous_evidence_at)
 AND c.merchant_config_at=sqlc.arg(merchant_config_at)
 AND m.updated_at=sqlc.arg(merchant_config_at)
 AND sqlc.arg(expires_at)::timestamptz>clock_timestamp()
 AND length(sqlc.arg(reference)::text) BETWEEN 1 AND 500;

-- name: PlatformConvergeRecurringRuntime :exec
UPDATE payment_recurring_capabilities c
SET runtime_available=sqlc.arg(adapter_available)::boolean AND c.adapter_key=sqlc.arg(adapter_key)::text AND c.merchant_approved
 AND c.evidence_at IS NOT NULL AND c.evidence_expires_at IS NOT NULL AND c.evidence_expires_at>now()
 AND EXISTS(SELECT 1 FROM payment_merchants m WHERE m.id=c.merchant_id AND m.provider=sqlc.arg(provider)::text AND m.enabled AND m.updated_at=c.merchant_config_at),
 updated_at=clock_timestamp()
WHERE c.merchant_id=sqlc.arg(merchant_id)
 AND c.runtime_available IS DISTINCT FROM (sqlc.arg(adapter_available)::boolean AND c.adapter_key=sqlc.arg(adapter_key)::text AND c.merchant_approved
 AND c.evidence_at IS NOT NULL AND c.evidence_expires_at IS NOT NULL AND c.evidence_expires_at>now()
 AND EXISTS(SELECT 1 FROM payment_merchants m WHERE m.id=c.merchant_id AND m.provider=sqlc.arg(provider)::text AND m.enabled AND m.updated_at=c.merchant_config_at));
