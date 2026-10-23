CREATE FUNCTION urmail_hold_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE held boolean;
BEGIN
 IF TG_TABLE_NAME='archive_entries' THEN
  PERFORM pg_advisory_xact_lock(OLD.tenant_id);
  IF OLD.legal_hold AND (TG_OP='DELETE' OR NEW.deleted_at IS NOT NULL OR NEW.mailbox_id IS DISTINCT FROM OLD.mailbox_id OR NEW.tenant_id<>OLD.tenant_id OR NEW.path<>OLD.path) THEN RAISE EXCEPTION 'LEGAL_HOLD' USING ERRCODE='23514'; END IF;
  IF TG_OP='UPDATE' AND NEW.legal_hold AND NEW.deleted_at IS NOT NULL THEN RAISE EXCEPTION 'LEGAL_HOLD_DELETED' USING ERRCODE='23514'; END IF;
 ELSIF TG_TABLE_NAME='mailboxes' THEN
  IF TG_OP='UPDATE' AND NEW.status<>'deleted' THEN RETURN NEW; END IF;
  PERFORM pg_advisory_xact_lock(OLD.tenant_id);
  SELECT EXISTS(SELECT 1 FROM archive_entries WHERE tenant_id=OLD.tenant_id AND mailbox_id=OLD.id AND legal_hold) INTO held;
  IF held THEN RAISE EXCEPTION 'LEGAL_HOLD' USING ERRCODE='23514'; END IF;
 ELSIF TG_TABLE_NAME='domains' THEN
  PERFORM pg_advisory_xact_lock(OLD.tenant_id);
  IF EXISTS(SELECT 1 FROM archive_entries a JOIN mailboxes m ON m.id=a.mailbox_id AND m.tenant_id=a.tenant_id WHERE m.tenant_id=OLD.tenant_id AND m.domain_id=OLD.id AND a.legal_hold) THEN RAISE EXCEPTION 'LEGAL_HOLD' USING ERRCODE='23514'; END IF;
 ELSE
  PERFORM pg_advisory_xact_lock(OLD.id);
  IF EXISTS(SELECT 1 FROM archive_entries WHERE tenant_id=OLD.id AND legal_hold) THEN RAISE EXCEPTION 'LEGAL_HOLD' USING ERRCODE='23514'; END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_hold_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_hold_guard() FROM PUBLIC;
CREATE TRIGGER archive_hold_guard BEFORE DELETE OR UPDATE ON archive_entries FOR EACH ROW EXECUTE FUNCTION urmail_hold_guard();
CREATE TRIGGER mailbox_hold_guard BEFORE DELETE OR UPDATE OF status ON mailboxes FOR EACH ROW EXECUTE FUNCTION urmail_hold_guard();
CREATE TRIGGER domain_hold_guard BEFORE DELETE ON domains FOR EACH ROW EXECUTE FUNCTION urmail_hold_guard();
CREATE TRIGGER tenant_hold_guard BEFORE DELETE ON tenants FOR EACH ROW EXECUTE FUNCTION urmail_hold_guard();
