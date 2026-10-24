-- Roundcube metadata lives in the same database, isolated from platform data.
CREATE SCHEMA IF NOT EXISTS roundcube AUTHORIZATION urmail_roundcube;
REVOKE ALL ON SCHEMA roundcube FROM PUBLIC,urmail_app,urmail_dovecot;

CREATE TABLE mailbox_sessions (
 token_hash text PRIMARY KEY CHECK(token_hash ~ '^[a-f0-9]{64}$'),
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 mailbox_id bigint NOT NULL,
 password_version text NOT NULL,
 expires_at timestamptz NOT NULL,
 revoked_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,mailbox_id) REFERENCES mailboxes(tenant_id,id),
 UNIQUE(tenant_id,mailbox_id,token_hash)
);
CREATE TABLE mailbox_operation_tickets (
 token_hash text PRIMARY KEY CHECK(token_hash ~ '^[a-f0-9]{64}$'),
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 session_hash text NOT NULL,
 scope text NOT NULL CHECK(scope IN('ai_context','sieve')),
 folder text NOT NULL DEFAULT '',
 message_uid bigint NOT NULL DEFAULT 0 CHECK(message_uid BETWEEN 0 AND 4294967295),
 uid_validity bigint NOT NULL DEFAULT 0 CHECK(uid_validity BETWEEN 0 AND 4294967295),
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,mailbox_id,session_hash) REFERENCES mailbox_sessions(tenant_id,mailbox_id,token_hash)
);
CREATE TABLE roundcube_bridge_requests (
 nonce text PRIMARY KEY CHECK(length(nonce) BETWEEN 32 AND 128),
 expires_at timestamptz NOT NULL
);
ALTER TABLE mailbox_sessions OWNER TO urmail_platform;
ALTER TABLE mailbox_operation_tickets OWNER TO urmail_platform;
ALTER TABLE roundcube_bridge_requests OWNER TO urmail_platform;
ALTER TABLE mailbox_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_operation_tickets ENABLE ROW LEVEL SECURITY;
ALTER TABLE roundcube_bridge_requests ENABLE ROW LEVEL SECURITY;
-- No app policies/grants: mailbox principals cannot be minted by a panel token.
CREATE INDEX ON mailbox_sessions(tenant_id,mailbox_id,expires_at);
CREATE INDEX ON mailbox_operation_tickets(expires_at);
CREATE FUNCTION urmail_revoke_mailbox_sessions() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_TABLE_NAME='mailboxes' THEN
  IF NEW.password_hash IS DISTINCT FROM OLD.password_hash OR NEW.status IS DISTINCT FROM OLD.status THEN
   UPDATE mailbox_sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE tenant_id=NEW.tenant_id AND mailbox_id=NEW.id;
  END IF;
 ELSIF TG_TABLE_NAME='domains' AND NEW.status IS DISTINCT FROM OLD.status THEN
  UPDATE mailbox_sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE tenant_id=NEW.tenant_id AND mailbox_id IN(SELECT id FROM mailboxes WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.id);
 ELSIF TG_TABLE_NAME='tenants' AND NEW.status IS DISTINCT FROM OLD.status THEN
  UPDATE mailbox_sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE tenant_id=NEW.id;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_revoke_mailbox_sessions() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_revoke_mailbox_sessions() FROM PUBLIC;
CREATE TRIGGER revoke_mailbox_sessions AFTER UPDATE ON mailboxes FOR EACH ROW EXECUTE FUNCTION urmail_revoke_mailbox_sessions();
CREATE TRIGGER revoke_domain_mailbox_sessions AFTER UPDATE OF status ON domains FOR EACH ROW EXECUTE FUNCTION urmail_revoke_mailbox_sessions();
CREATE TRIGGER revoke_tenant_mailbox_sessions AFTER UPDATE OF status ON tenants FOR EACH ROW EXECUTE FUNCTION urmail_revoke_mailbox_sessions();
-- Earlier default privileges grant app DML on future migration tables. Revoke
-- those defaults explicitly: service credentials must remain outside panel RLS.
REVOKE ALL ON mailbox_sessions,mailbox_operation_tickets,roundcube_bridge_requests FROM PUBLIC,urmail_app,urmail_dovecot,urmail_roundcube;
-- A bridge credential stays restricted to the Sieve API; it never becomes a
-- panel session. Its parent mailbox session remains the revocation authority.
ALTER TABLE mailbox_operation_tickets ADD COLUMN kind text NOT NULL DEFAULT 'operation' CHECK(kind IN('operation','sieve_bridge'));
