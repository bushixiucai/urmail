-- Reuse each existing recall fact as a bounded, explicitly reviewed local action.
ALTER TABLE recall_requests DROP CONSTRAINT recall_requests_status_check;
ALTER TABLE recall_requests ADD CONSTRAINT recall_requests_status_check
 CHECK(status IN('preview','pending','delivered','failed','expired','local_recalled','uncertain'));
ALTER TABLE recall_requests
 ADD COLUMN preview_actor_id bigint REFERENCES users(id) ON DELETE RESTRICT,
 ADD COLUMN preview_token text CHECK(preview_token IS NULL OR preview_token ~ '^[0-9a-f-]{36}$'),
 ADD COLUMN preview_digest text CHECK(preview_digest IS NULL OR preview_digest ~ '^[0-9a-f]{64}$'),
 ADD COLUMN preview_snapshot bytea CHECK(preview_snapshot IS NULL OR octet_length(preview_snapshot)<=262144),
 ADD COLUMN preview_expires_at timestamptz,
 ADD COLUMN confirmed_at timestamptz,
 ADD COLUMN selected_copy_keys jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(selected_copy_keys)='array' AND jsonb_array_length(selected_copy_keys)<=100),
 ADD COLUMN attempt_epoch bigint NOT NULL DEFAULT 0 CHECK(attempt_epoch>=0),
 ADD COLUMN local_result jsonb NOT NULL DEFAULT '{"items":[]}' CHECK(jsonb_typeof(local_result)='object'),
 ADD COLUMN result_history jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(result_history)='array' AND jsonb_array_length(result_history)<=100);

CREATE OR REPLACE FUNCTION urmail_recall_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE actor bigint:=nullif(current_setting('urmail.user_id',true),'')::bigint;
 tenant bigint:=nullif(current_setting('urmail.tenant_id',true),'')::bigint;
 service boolean:=current_user='urmail_platform'; source archive_entries; mailbox mailboxes; snapshot jsonb;
BEGIN
 IF TG_OP='UPDATE' AND (NEW.tenant_id<>OLD.tenant_id OR NEW.archive_entry_id IS DISTINCT FROM OLD.archive_entry_id
 OR NEW.mailbox_id IS DISTINCT FROM OLD.mailbox_id OR NEW.message_id<>OLD.message_id OR NEW.requested_by<>OLD.requested_by) THEN
  RAISE EXCEPTION 'RECALL_IDENTITY_IMMUTABLE' USING ERRCODE='23514';
 END IF;
 IF NEW.status='preview' THEN
  IF TG_OP='INSERT' AND NEW.requested_by IS DISTINCT FROM actor THEN RAISE EXCEPTION 'RECALL_ORIGINAL_ACTOR_REQUIRED' USING ERRCODE='23514';END IF;
  IF NEW.tenant_id IS DISTINCT FROM tenant OR NEW.preview_actor_id IS DISTINCT FROM actor OR NOT EXISTS(
   SELECT 1 FROM user_tenant_roles r JOIN users u ON u.id=r.user_id JOIN tenants t ON t.id=r.tenant_id
   WHERE r.tenant_id=tenant AND r.user_id=actor AND r.role IN('owner','admin') AND u.status='active' AND t.status='active') THEN
   RAISE EXCEPTION 'RECALL_PREVIEW_AUTHORITY_REQUIRED' USING ERRCODE='42501'; END IF;
  IF TG_OP='UPDATE' AND OLD.status='pending' AND OLD.preview_token IS NOT NULL THEN
   RAISE EXCEPTION 'RECALL_IN_PROGRESS' USING ERRCODE='23514'; END IF;
  IF NEW.preview_token IS NULL OR NEW.preview_digest IS NULL OR NEW.preview_snapshot IS NULL
   OR NEW.preview_expires_at IS NULL OR NEW.preview_expires_at<=statement_timestamp() OR NEW.preview_expires_at>statement_timestamp()+interval '5 minutes 1 second'
   OR NEW.confirmed_at IS NOT NULL OR NEW.selected_copy_keys<>'[]'::jsonb
   OR NEW.attempt_epoch<>(CASE WHEN TG_OP='INSERT' THEN 1 ELSE OLD.attempt_epoch+1 END) THEN
   RAISE EXCEPTION 'RECALL_PREVIEW_PROOF_REQUIRED' USING ERRCODE='23514'; END IF;
  IF encode(sha256(NEW.preview_snapshot),'hex')<>NEW.preview_digest THEN RAISE EXCEPTION 'RECALL_PREVIEW_DIGEST_INVALID' USING ERRCODE='23514'; END IF;
  snapshot:=convert_from(NEW.preview_snapshot,'UTF8')::jsonb;
  SELECT * INTO source FROM archive_entries a WHERE a.tenant_id=tenant AND a.id=NEW.archive_entry_id FOR SHARE;
  SELECT * INTO mailbox FROM mailboxes m WHERE m.tenant_id=tenant AND m.id=source.mailbox_id FOR SHARE;
  IF source.id IS NULL OR mailbox.id IS NULL OR NEW.mailbox_id IS DISTINCT FROM mailbox.id OR NEW.message_id<>source.message_id
   OR snapshot->>'tenant_id' IS DISTINCT FROM tenant::text OR snapshot->>'actor_id' IS DISTINCT FROM actor::text OR snapshot->>'entry_id' IS DISTINCT FROM source.id::text
   OR snapshot->>'mailbox_id' IS DISTINCT FROM mailbox.id::text OR snapshot->>'address' IS DISTINCT FROM mailbox.address OR snapshot->>'message_id' IS DISTINCT FROM source.message_id
   OR jsonb_typeof(snapshot->'copies') IS DISTINCT FROM 'array' OR jsonb_array_length(snapshot->'copies')>100 THEN
   RAISE EXCEPTION 'RECALL_PREVIEW_TARGET_MISMATCH' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF TG_OP='INSERT' THEN
  -- Historical service fixtures/imports remain representable, but the agent
  -- will never mutate a pending row without the new reviewed proof.
  IF NOT service OR NEW.status<>'pending' THEN RAISE EXCEPTION 'RECALL_PREVIEW_REQUIRED' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF NEW.status='pending' AND OLD.status='preview' THEN
  IF NEW.tenant_id IS DISTINCT FROM tenant OR NEW.preview_actor_id IS DISTINCT FROM actor
   OR NEW.preview_actor_id IS DISTINCT FROM OLD.preview_actor_id OR NEW.preview_token IS DISTINCT FROM OLD.preview_token OR NEW.preview_digest IS DISTINCT FROM OLD.preview_digest
   OR NEW.preview_snapshot IS DISTINCT FROM OLD.preview_snapshot OR NEW.preview_expires_at IS DISTINCT FROM OLD.preview_expires_at
   OR NEW.attempt_epoch<>OLD.attempt_epoch OR NEW.preview_expires_at<=statement_timestamp() OR NEW.confirmed_at IS NULL
   OR jsonb_array_length(NEW.selected_copy_keys)=0
   OR NOT EXISTS(SELECT 1 FROM user_tenant_roles r JOIN users u ON u.id=r.user_id WHERE r.tenant_id=tenant AND r.user_id=actor AND r.role IN('owner','admin') AND u.status='active') THEN
   RAISE EXCEPTION 'RECALL_CONFIRMATION_PROOF_REQUIRED' USING ERRCODE='23514'; END IF;
  snapshot:=convert_from(NEW.preview_snapshot,'UTF8')::jsonb;
  IF EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.selected_copy_keys) k WHERE jsonb_typeof(k)<>'string'
   OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(snapshot->'copies') c
    WHERE k=to_jsonb((c->>'guid')||':'||(c->>'uid_validity')||':'||(c->>'uid'))))
   OR (SELECT count(*) FROM jsonb_array_elements(NEW.selected_copy_keys))<>(SELECT count(DISTINCT value) FROM jsonb_array_elements(NEW.selected_copy_keys)) THEN
   RAISE EXCEPTION 'RECALL_COPY_SELECTION_INVALID' USING ERRCODE='23514'; END IF;
  IF EXISTS(SELECT 1 FROM archive_entries a WHERE a.tenant_id=tenant AND a.mailbox_id=NEW.mailbox_id
   AND a.message_id=NEW.message_id AND a.legal_hold AND a.deleted_at IS NULL) THEN
   RAISE EXCEPTION 'RECALL_HELD_COPY' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF service AND OLD.status IN('pending','preview','uncertain') AND NEW.status IN('failed','expired','local_recalled','uncertain') THEN
  IF NEW.status='local_recalled' AND (OLD.status<>'pending' OR OLD.confirmed_at IS NULL OR OLD.preview_token IS NULL OR NEW.completed_at IS NULL) THEN
   RAISE EXCEPTION 'RECALL_VERIFIED_LOCAL_PROOF_REQUIRED' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 RAISE EXCEPTION 'RECALL_STATE_TRANSITION_REJECTED' USING ERRCODE='23514';
END $$;
ALTER FUNCTION urmail_recall_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_recall_guard() FROM PUBLIC;

-- A fresh explicit review may be requested by another currently authorized
-- administrator; retain requested_by as the original historical principal.
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_archive_activity_notice()'::regprocedure) INTO definition;
 EXECUTE replace(definition,'NEW.requested_by','coalesce((to_jsonb(NEW)->>''preview_actor_id'')::bigint,NEW.requested_by)');
 SELECT pg_get_functiondef('urmail_archive_notification_visible(bigint,bigint,text,jsonb)'::regprocedure) INTO definition;
 EXECUTE replace(definition,'r.requested_by','coalesce(r.preview_actor_id,r.requested_by)');
END $$;
