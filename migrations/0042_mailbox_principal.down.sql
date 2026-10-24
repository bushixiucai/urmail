DROP TRIGGER revoke_mailbox_sessions ON mailboxes;
DROP TRIGGER revoke_domain_mailbox_sessions ON domains;
DROP TRIGGER revoke_tenant_mailbox_sessions ON tenants;
DROP FUNCTION urmail_revoke_mailbox_sessions();
DROP TABLE roundcube_bridge_requests;
DROP TABLE mailbox_operation_tickets;
DROP TABLE mailbox_sessions;
-- The installer owns vendor metadata and must explicitly export it before
-- removing the dedicated schema. Rolling back application grants preserves it.
