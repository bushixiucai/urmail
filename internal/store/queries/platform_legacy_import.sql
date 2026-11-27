-- Explicit migration operator authority; runtime API callers cannot invoke this workflow.
-- name: PlatformLockLegacyImportTarget :one
-- scope: platform

SELECT t.id
FROM tenants t
JOIN users actor ON actor.id=sqlc.arg(actor_id) AND actor.status='active'
JOIN platform_admins administrator ON administrator.user_id=actor.id
WHERE t.id=sqlc.arg(tenant_id) AND t.status='active';

-- name: PlatformLegacyImportTargetFacts :one
-- scope: platform

SELECT
 (SELECT count(*) FROM domains fact WHERE fact.tenant_id=sqlc.arg(tenant_id)) AS domains,
 (SELECT count(*) FROM mailboxes fact WHERE fact.tenant_id=sqlc.arg(tenant_id)) AS mailboxes,
 (SELECT count(*) FROM mailbox_aliases fact WHERE fact.tenant_id=sqlc.arg(tenant_id)) AS aliases,
 (SELECT count(*) FROM audit_logs fact WHERE fact.tenant_id=sqlc.arg(tenant_id) AND fact.action='IMPORT legacy.mail') AS imports;

-- name: PlatformCreateLegacyAlias :one
-- scope: platform

INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,kind,active,sender_logins)
VALUES(sqlc.arg(tenant_id),sqlc.arg(domain_id),sqlc.arg(local_part),
 sqlc.arg(target),'alias',true,sqlc.arg(sender_logins))
RETURNING *;

-- name: PlatformCreateLegacyDomain :one
-- scope: platform

INSERT INTO domains(tenant_id,fqdn,mode,status,verification_token,tls_mode,dkim_selector,provisioning_state,dns_explicit)
VALUES(sqlc.arg(tenant_id),sqlc.arg(fqdn),'delegated','pending_dns',
 sqlc.arg(verification_token),'letsencrypt','mail','pending',false)
RETURNING id;

-- name: PlatformCreateLegacyMailbox :one
-- scope: platform

INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled,sieve_mode)
VALUES(sqlc.arg(tenant_id),sqlc.arg(domain_id),sqlc.arg(local_part),sqlc.arg(address),
 sqlc.arg(password_hash),sqlc.arg(quota_bytes),'active',true,'legacy_v80')
RETURNING id;

-- name: PlatformLockLegacyDKIMDomains :many
-- scope: platform

SELECT d.id,d.fqdn,d.dkim_selector,d.dkim_private_key,d.status,d.provisioning_state
FROM domains d
WHERE d.tenant_id=sqlc.arg(tenant_id) AND EXISTS(SELECT 1 FROM json_each(sqlc.arg(domain_names)) je WHERE je.value = d.fqdn)
 AND EXISTS(SELECT 1 FROM audit_logs a WHERE a.tenant_id=d.tenant_id AND a.action='IMPORT legacy.mail'
  AND json_extract(a.detail, '$.private_proof_sha256')=sqlc.arg(core_proof_sha256) AND json_extract(a.detail, '$.commit_outcome')='committed')
ORDER BY d.fqdn;

-- name: PlatformSetLegacyDKIM :execrows
-- scope: platform

UPDATE domains SET dkim_selector=sqlc.arg(selector),dkim_private_key=sqlc.arg(private_key)
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(domain_id)
 AND status='pending_dns' AND provisioning_state='pending' AND dkim_private_key IS NULL;

-- Promote imported delegated domains for mail routing without claiming managed DNS.
-- mode and dns_explicit stay delegated/false so NSD reconcile emits no zones.
-- name: PlatformActivateLegacyDelegatedMail :execrows
-- scope: platform

UPDATE domains SET
 status='active',
 verified_at=COALESCE(verified_at, (CAST(strftime('%s','now') AS INTEGER)*1000000)),
 provisioning_state='ready',
 updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=sqlc.arg(tenant_id)
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
   AND json_extract(a.detail, '$.private_proof_sha256')=sqlc.arg(core_proof_sha256)
   AND json_extract(a.detail, '$.commit_outcome')='committed'
 )
 AND EXISTS(
  SELECT 1 FROM audit_logs a
  WHERE a.tenant_id=domains.tenant_id
   AND a.action='IMPORT legacy.dkim'
   AND json_extract(a.detail, '$.core_proof_sha256')=sqlc.arg(core_proof_sha256)
   AND json_extract(a.detail, '$.commit_outcome')='committed'
 );
