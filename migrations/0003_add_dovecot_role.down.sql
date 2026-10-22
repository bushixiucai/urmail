-- 0003_add_dovecot_role.down.sql
--
-- Rollback 0003: revoke permissions from urmail_dovecot.
-- Roles themselves are left in place (consistent with 0001_init.down.sql convention).

REVOKE SELECT ON mailboxes, domains FROM urmail_dovecot;
-- The role and its BYPASSRLS attribute are cluster-level bootstrap state and
-- are intentionally left alone on down migration.
