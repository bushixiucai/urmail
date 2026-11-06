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

-- name: PlatformLockLegacyDKIMDomains :many
SELECT d.id,d.fqdn,d.dkim_selector,d.dkim_private_key,d.status,d.provisioning_state
FROM domains d
WHERE d.tenant_id=sqlc.arg(tenant_id)::bigint AND d.fqdn=ANY(sqlc.arg(domain_names)::text[])
 AND EXISTS(SELECT 1 FROM audit_logs a WHERE a.tenant_id=d.tenant_id AND a.action='IMPORT legacy.mail'
  AND a.detail->>'private_proof_sha256'=sqlc.arg(core_proof_sha256)::text AND a.detail->>'commit_outcome'='committed')
ORDER BY d.fqdn FOR UPDATE OF d;

-- name: PlatformSetLegacyDKIM :execrows
UPDATE domains SET dkim_selector=sqlc.arg(selector)::text,dkim_private_key=sqlc.arg(private_key)::text
WHERE tenant_id=sqlc.arg(tenant_id)::bigint AND id=sqlc.arg(domain_id)::bigint
 AND status='pending_dns' AND provisioning_state='pending' AND dkim_private_key IS NULL;

-- Promote imported delegated domains for mail routing without claiming managed DNS.
-- mode and dns_explicit stay delegated/false so NSD reconcile emits no zones.
-- name: PlatformActivateLegacyDelegatedMail :execrows
UPDATE domains SET
 status='active',
 verified_at=COALESCE(verified_at, statement_timestamp()),
 provisioning_state='ready',
 updated_at=statement_timestamp()
WHERE tenant_id=sqlc.arg(tenant_id)::bigint
 AND mode='delegated'
 AND status='pending_dns'
 AND provisioning_state='pending'
 AND dns_explicit=false
 AND dkim_private_key IS NOT NULL
 AND dkim_private_key <> ''
 AND EXISTS(
  SELECT 1 FROM audit_logs a
  WHERE a.tenant_id=domains.tenant_id
   AND a.action='IMPORT legacy.mail'
   AND a.detail->>'private_proof_sha256'=sqlc.arg(core_proof_sha256)::text
   AND a.detail->>'commit_outcome'='committed'
 )
 AND EXISTS(
  SELECT 1 FROM audit_logs a
  WHERE a.tenant_id=domains.tenant_id
   AND a.action='IMPORT legacy.dkim'
   AND a.detail->>'core_proof_sha256'=sqlc.arg(core_proof_sha256)::text
   AND a.detail->>'commit_outcome'='committed'
 );
