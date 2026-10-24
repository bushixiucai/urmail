-- Migration 0032: verified_at is required whenever a domain is active.
--
-- The normal verification flow has always written verified_at alongside the
-- active status, so an active row without one was never produced by the
-- product: it can only come from manual seeding or an older bug. Such a row
-- is demoted to pending_dns (instead of being silently blessed with a fake
-- timestamp) so the operator has to re-verify control before the domain
-- renders into Postfix/NSD/nginx again. A future timestamp is rejected by the
-- API guard, so it is treated the same way.
--
-- The constraint is added NOT VALID and validated in a second statement:
-- ADD CONSTRAINT ... NOT VALID takes only a brief metadata lock, so the
-- upgrade can no longer be blocked or aborted by a full-table scan on a
-- large tenants table.
UPDATE domains SET status = 'pending_dns'
 WHERE status = 'active'
   AND (verified_at IS NULL OR verified_at > now());

ALTER TABLE domains ADD CONSTRAINT domains_active_requires_verified_at
    CHECK (status <> 'active' OR verified_at IS NOT NULL) NOT VALID;
ALTER TABLE domains VALIDATE CONSTRAINT domains_active_requires_verified_at;
