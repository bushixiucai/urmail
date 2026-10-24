-- Reuse existing support records and make authorship a database boundary.
ALTER TABLE tickets ADD COLUMN assigned_to bigint REFERENCES users(id), ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE tickets ADD CONSTRAINT tickets_tenant_id_id_unique UNIQUE(tenant_id,id);
ALTER TABLE tickets ADD CONSTRAINT tickets_subject_bounded CHECK(length(subject) BETWEEN 1 AND 200);
ALTER TABLE tickets ADD CONSTRAINT tickets_priority_allowed CHECK(priority IN('low','normal','high'));
ALTER TABLE ticket_messages ADD COLUMN tenant_id bigint;
UPDATE ticket_messages m SET tenant_id=t.tenant_id FROM tickets t WHERE t.id=m.ticket_id;
ALTER TABLE ticket_messages ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE ticket_messages ADD CONSTRAINT ticket_messages_tenant_fk FOREIGN KEY(tenant_id,ticket_id) REFERENCES tickets(tenant_id,id) ON DELETE CASCADE;
ALTER TABLE ticket_messages ADD CONSTRAINT ticket_messages_body_bounded CHECK(octet_length(body) BETWEEN 1 AND 65536);
ALTER TABLE ticket_messages ADD CONSTRAINT ticket_messages_tenant_id_id_unique UNIQUE(tenant_id,id);
DROP POLICY tickets_isolation ON tickets;
CREATE POLICY tickets_isolation ON tickets USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND opened_by=nullif(current_setting('urmail.user_id',true),'')::bigint)
WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND opened_by=nullif(current_setting('urmail.user_id',true),'')::bigint);
DROP POLICY ticket_messages_isolation ON ticket_messages;
CREATE POLICY ticket_messages_isolation ON ticket_messages USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND EXISTS(SELECT 1 FROM tickets t WHERE t.tenant_id=ticket_messages.tenant_id AND t.id=ticket_id))
WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND author_id=nullif(current_setting('urmail.user_id',true),'')::bigint AND NOT is_staff AND EXISTS(SELECT 1 FROM tickets t WHERE t.tenant_id=ticket_messages.tenant_id AND t.id=ticket_id));
REVOKE UPDATE,DELETE ON tickets,ticket_messages FROM urmail_app;
GRANT UPDATE(status) ON tickets TO urmail_app;
CREATE TABLE ticket_attachments(
 id bigserial PRIMARY KEY,tenant_id bigint NOT NULL,message_id bigint NOT NULL,
 filename text NOT NULL CHECK(length(filename) BETWEEN 1 AND 200 AND filename !~ '[\r\n/\\]'),
 content_type text NOT NULL CHECK(content_type IN('text/plain','image/png','image/jpeg','application/pdf')),
 content bytea NOT NULL CHECK(octet_length(content) BETWEEN 1 AND 2097152),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,message_id) REFERENCES ticket_messages(tenant_id,id) ON DELETE CASCADE
);
ALTER TABLE ticket_attachments OWNER TO urmail_platform;
REVOKE ALL ON ticket_attachments FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE ticket_attachments ENABLE ROW LEVEL SECURITY;
CREATE POLICY ticket_attachments_isolation ON ticket_attachments USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND EXISTS(SELECT 1 FROM ticket_messages m WHERE m.tenant_id=ticket_attachments.tenant_id AND m.id=message_id));
GRANT SELECT,INSERT ON ticket_attachments TO urmail_app;
GRANT USAGE,SELECT ON SEQUENCE ticket_attachments_id_seq TO urmail_app;
CREATE FUNCTION urmail_require_open_ticket() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF current_setting('role',true)='urmail_app' THEN
  PERFORM 1 FROM tickets WHERE tenant_id=NEW.tenant_id AND id=NEW.ticket_id AND status IN('open','pending') FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'TICKET_CLOSED' USING ERRCODE='42501'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ticket_messages_open_guard BEFORE INSERT ON ticket_messages FOR EACH ROW EXECUTE FUNCTION urmail_require_open_ticket();
ALTER TABLE announcements ADD COLUMN state text NOT NULL DEFAULT 'draft' CHECK(state IN('draft','published','withdrawn'));
UPDATE announcements SET state='published' WHERE published_at IS NOT NULL;
ALTER TABLE announcements ADD COLUMN ends_at timestamptz;
ALTER TABLE announcements ADD CONSTRAINT announcements_time_order CHECK(ends_at IS NULL OR published_at IS NULL OR ends_at>published_at);
CREATE POLICY announcements_visibility ON announcements AS RESTRICTIVE FOR SELECT USING(
 state='published' AND published_at<=now() AND (ends_at IS NULL OR ends_at>now()) AND EXISTS(SELECT 1 FROM tenants WHERE id=nullif(current_setting('urmail.tenant_id',true),'')::bigint));
REVOKE INSERT ON tickets,ticket_messages FROM urmail_app;
GRANT INSERT(tenant_id,opened_by,subject,priority) ON tickets TO urmail_app;
GRANT INSERT(tenant_id,ticket_id,author_id,body,is_staff) ON ticket_messages TO urmail_app;

CREATE FUNCTION urmail_support_reply_notification() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE opener bigint;
BEGIN
 UPDATE tickets SET updated_at=now() WHERE tenant_id=NEW.tenant_id AND id=NEW.ticket_id RETURNING opened_by INTO opener;
 IF NEW.is_staff THEN
  INSERT INTO notification_outbox(tenant_id,user_id,kind,message_key,params,dedupe_key,channel,state,delivered_at)
  VALUES(NEW.tenant_id,opener,'support_reply','notification.support_reply',jsonb_build_object('ticket',NEW.ticket_id),'support-reply:'||NEW.id,'in_app','delivered',now()) ON CONFLICT(dedupe_key) DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_support_reply_notification() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_support_reply_notification() FROM PUBLIC;
CREATE TRIGGER support_reply_notification AFTER INSERT ON ticket_messages FOR EACH ROW EXECUTE FUNCTION urmail_support_reply_notification();
DROP POLICY ticket_attachments_isolation ON ticket_attachments;
CREATE POLICY ticket_attachments_isolation ON ticket_attachments
 USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND EXISTS(SELECT 1 FROM ticket_messages m WHERE m.tenant_id=ticket_attachments.tenant_id AND m.id=message_id))
 WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND EXISTS(SELECT 1 FROM ticket_messages m WHERE m.tenant_id=ticket_attachments.tenant_id AND m.id=message_id AND m.author_id=nullif(current_setting('urmail.user_id',true),'')::bigint AND NOT m.is_staff));
