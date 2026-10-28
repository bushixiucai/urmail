-- Bind new previews to the immutable message identity as well as mailbox
-- generation/UID. Existing 0142 proofs without that identity fail closed in
-- the agent and must be explicitly previewed again.
CREATE FUNCTION urmail_recall_message_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE snapshot jsonb;
BEGIN
 IF NEW.status='preview' THEN
  snapshot:=convert_from(NEW.preview_snapshot,'UTF8')::jsonb;
  IF EXISTS(SELECT 1 FROM jsonb_array_elements(snapshot->'copies') copy
   WHERE jsonb_typeof(copy->'message_guid') IS DISTINCT FROM 'string'
    OR length(copy->>'message_guid') NOT BETWEEN 1 AND 512
    OR NOT (copy->>'message_guid' ~ '^[A-Za-z0-9][A-Za-z0-9._,+:=@-]*$')) THEN
   RAISE EXCEPTION 'RECALL_IMMUTABLE_MESSAGE_IDENTITY_REQUIRED' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_recall_message_identity_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_recall_message_identity_guard() FROM PUBLIC;
CREATE TRIGGER recall_message_identity_guard BEFORE INSERT OR UPDATE ON recall_requests
 FOR EACH ROW EXECUTE FUNCTION urmail_recall_message_identity_guard();
