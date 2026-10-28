-- name: PlatformCreateRecurringSetup :one
INSERT INTO recurring_setup_intents(tenant_id,subscription_id,actor_id,merchant_id,request_key,request_id,quote_digest,currency,expected_period_end,merchant_config_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING *;

-- name: PlatformGetRecurringSetupByKey :one
SELECT * FROM recurring_setup_intents WHERE tenant_id=$1 AND actor_id=$2 AND request_key=$3;

-- name: PlatformGetRecurringSetup :one
SELECT * FROM recurring_setup_intents WHERE tenant_id=$1 AND actor_id=$2 AND id=$3;

-- name: PlatformLockRecurringSetup :one
SELECT * FROM recurring_setup_intents WHERE tenant_id=$1 AND actor_id=$2 AND id=$3 FOR UPDATE;

-- name: PlatformListRecurringSetups :many
SELECT * FROM recurring_setup_intents WHERE tenant_id=$1 AND actor_id=$2 ORDER BY id DESC LIMIT 20;

-- name: PlatformUpdateRecurringSetup :one
UPDATE recurring_setup_intents SET state=sqlc.arg(state),version=version+1,
 provider_reference=sqlc.arg(provider_reference),hosted_url=sqlc.arg(hosted_url),expires_at=sqlc.arg(expires_at),
 binding_id=sqlc.narg(binding_id),last_error=sqlc.arg(last_error),updated_at=now()
WHERE tenant_id=sqlc.arg(tenant_id) AND actor_id=sqlc.arg(actor_id) AND id=sqlc.arg(id) AND version=sqlc.arg(expected_version)
RETURNING *;

-- name: PlatformLockRecurringSetupActor :one
SELECT tu.role FROM user_tenant_roles tu JOIN users u ON u.id=tu.user_id JOIN tenants t ON t.id=tu.tenant_id
WHERE tu.tenant_id=$1 AND tu.user_id=$2 AND u.status='active' AND t.status='active' AND tu.role IN('owner','admin','billing')
FOR SHARE OF tu,u,t;

-- name: PlatformLockRecurringSetupMerchant :one
SELECT * FROM payment_merchants WHERE id=$1 FOR SHARE;

-- name: PlatformCountOpenRecurringSetups :one
SELECT count(*) FROM recurring_setup_intents WHERE tenant_id=$1 AND state IN('creating','ready','uncertain') AND expires_at>now();

-- name: PlatformAuditRecurringSetup :exec
INSERT INTO audit_logs(tenant_id,actor_user_id,action,target_type,target_id,detail)
SELECT tenant_id,actor_id,sqlc.arg(action),'recurring_setup',id::text,jsonb_build_object('state',state,'version',version,'merchant_id',merchant_id,'subscription_id',subscription_id,'request_id',request_id)
FROM recurring_setup_intents s WHERE s.tenant_id=sqlc.arg(tenant_id) AND s.actor_id=sqlc.arg(actor_id) AND s.id=sqlc.arg(id);

-- name: PlatformLockRecurringSetupCapability :one
SELECT merchant_id FROM payment_recurring_capabilities WHERE merchant_id=$1 FOR SHARE;
