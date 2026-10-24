-- Receiving aliases do not implicitly authorize SMTP sender identities.
ALTER TABLE mailbox_aliases ADD COLUMN sender_logins text[] NOT NULL DEFAULT '{}';
ALTER TABLE mailbox_aliases ADD CONSTRAINT mailbox_aliases_sender_logins_shape
 CHECK (cardinality(sender_logins)<=100 AND array_position(sender_logins,NULL) IS NULL
 AND (cardinality(sender_logins)=0 OR array_ndims(sender_logins)=1));
CREATE FUNCTION urmail_alias_sender_logins_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE login text;
BEGIN
 -- A routing edit must not carry an invisible legacy sender grant forward.
 IF TG_OP='UPDATE' AND (NEW.target,NEW.local_part,NEW.kind,NEW.domain_id) IS DISTINCT FROM (OLD.target,OLD.local_part,OLD.kind,OLD.domain_id)
  AND NEW.sender_logins IS NOT DISTINCT FROM OLD.sender_logins THEN
  NEW.sender_logins:='{}';
 END IF;
 IF NEW.kind='catchall' AND cardinality(NEW.sender_logins)>0 THEN
  RAISE EXCEPTION 'ALIAS_CATCHALL_SENDER_LOGIN_FORBIDDEN' USING ERRCODE='23514';
 END IF;
 FOREACH login IN ARRAY NEW.sender_logins LOOP
  IF login<>lower(login) OR length(login)>254 OR
   NOT EXISTS(SELECT 1 FROM public.mailboxes m WHERE m.tenant_id=NEW.tenant_id AND m.address=login AND m.status<>'deleted') THEN
   RAISE EXCEPTION 'ALIAS_SENDER_LOGIN_NOT_TENANT_MAILBOX' USING ERRCODE='23514';
  END IF;
 END LOOP;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION urmail_alias_sender_logins_guard() FROM PUBLIC;
CREATE TRIGGER alias_sender_logins_guard BEFORE INSERT OR UPDATE OF tenant_id,domain_id,local_part,target,kind,sender_logins ON mailbox_aliases
 FOR EACH ROW EXECUTE FUNCTION urmail_alias_sender_logins_guard();
