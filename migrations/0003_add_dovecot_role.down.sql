-- 0003_add_dovecot_role.down.sql
--
-- Rollback 0003: revoke permissions from urmail_dovecot.
-- Roles themselves are left in place (consistent with 0001_init.down.sql convention).

REVOKE SELECT ON mailboxes, domains FROM urmail_dovecot;
REVOKE USAGE ON SCHEMA public FROM urmail_dovecot;
ALTER ROLE urmail_dovecot NOBYPASSRLS;
