-- Explicit migration operator authority; runtime API callers cannot invoke this workflow.
-- name: PlatformLockLegacyImportTarget :one
SELECT t.id
FROM tenants t
JOIN users actor ON actor.id=sqlc.arg(actor_id)::bigint AND actor.status='active'
JOIN platform_admins administrator ON administrator.user_id=actor.id
WHERE t.id=sqlc.arg(tenant_id)::bigint AND t.status='active'
FOR UPDATE OF t
FOR SHARE OF actor,administrator;

-- name: PlatformLegacyImportTargetFacts :one
SELECT
 (SELECT count(*) FROM domains fact WHERE fact.tenant_id=sqlc.arg(tenant_id)::bigint)::bigint AS domains,
 (SELECT count(*) FROM mailboxes fact WHERE fact.tenant_id=sqlc.arg(tenant_id)::bigint)::bigint AS mailboxes,
 (SELECT count(*) FROM mailbox_aliases fact WHERE fact.tenant_id=sqlc.arg(tenant_id)::bigint)::bigint AS aliases,
 (SELECT count(*) FROM orders fact WHERE fact.tenant_id=sqlc.arg(tenant_id)::bigint)::bigint AS orders,
 (SELECT count(*) FROM payments fact WHERE fact.tenant_id=sqlc.arg(tenant_id)::bigint)::bigint AS payments,
 (SELECT count(*) FROM invoices fact WHERE fact.tenant_id=sqlc.arg(tenant_id)::bigint)::bigint AS invoices,
 (SELECT count(*) FROM subscription_terms fact WHERE fact.tenant_id=sqlc.arg(tenant_id)::bigint)::bigint AS paid_terms,
 (SELECT count(*) FROM billing_calendars fact WHERE fact.tenant_id=sqlc.arg(tenant_id)::bigint)::bigint AS billing_calendars,
 (SELECT count(*) FROM tenant_trials fact WHERE fact.tenant_id=sqlc.arg(tenant_id)::bigint)::bigint AS trials,
 (SELECT count(*) FROM audit_logs fact WHERE fact.tenant_id=sqlc.arg(tenant_id)::bigint AND fact.action='IMPORT legacy.mail')::bigint AS imports;

-- name: PlatformCreateLegacyAlias :one
INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,kind,active,sender_logins)
VALUES(sqlc.arg(tenant_id)::bigint,sqlc.arg(domain_id)::bigint,sqlc.arg(local_part)::text,
 sqlc.arg(target)::text,'alias',true,sqlc.arg(sender_logins)::text[])
RETURNING *;

-- name: PlatformCreateLegacyDomain :one
INSERT INTO domains(tenant_id,fqdn,mode,status,verification_token,tls_mode,dkim_selector,provisioning_state,dns_explicit)
VALUES(sqlc.arg(tenant_id)::bigint,sqlc.arg(fqdn)::text,'delegated','pending_dns',
 sqlc.arg(verification_token)::text,'letsencrypt','mail','pending',false)
RETURNING id;

-- name: PlatformCreateLegacyMailbox :one
INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled,sieve_mode)
VALUES(sqlc.arg(tenant_id)::bigint,sqlc.arg(domain_id)::bigint,sqlc.arg(local_part)::text,sqlc.arg(address)::text,
 sqlc.arg(password_hash)::text,sqlc.arg(quota_bytes)::bigint,'active',true,'legacy_v80')
RETURNING id;
