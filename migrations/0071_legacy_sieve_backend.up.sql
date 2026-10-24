-- Per-mailbox compatibility preserves source ManageSieve without disabling M7
-- structured Sieve for new mailboxes or allowing two writers for one mailbox.
ALTER TABLE mailboxes ADD COLUMN sieve_mode text NOT NULL DEFAULT 'managed'
 CHECK(sieve_mode IN('managed','legacy_v80'));
CREATE FUNCTION urmail_mailbox_sieve_mode_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE trusted boolean;
BEGIN
 trusted:=current_setting('role',true) IN('urmail_platform','urmail_migrate')
 OR(current_setting('role',true)='none' AND pg_has_role(session_user,'urmail_platform','USAGE'));
 IF TG_OP='INSERT' THEN
  IF NEW.sieve_mode='legacy_v80' AND NOT(trusted AND urmail_migration_entitlement_allowed(NEW.tenant_id)) THEN
   RAISE EXCEPTION 'LEGACY_SIEVE_IMPORT_AUTHORITY_REQUIRED' USING ERRCODE='42501';
  END IF;
 ELSE
  IF NEW.sieve_mode IS DISTINCT FROM OLD.sieve_mode THEN
   RAISE EXCEPTION 'SIEVE_BACKEND_TRANSITION_REQUIRES_REVIEW' USING ERRCODE='23514';
  END IF;
  IF OLD.sieve_mode='legacy_v80' AND NEW.sieve_script IS DISTINCT FROM OLD.sieve_script THEN
   RAISE EXCEPTION 'LEGACY_SIEVE_METADATA_READ_ONLY' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_mailbox_sieve_mode_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_mailbox_sieve_mode_guard() FROM PUBLIC;
CREATE TRIGGER mailbox_sieve_mode_guard BEFORE INSERT OR UPDATE ON mailboxes
 FOR EACH ROW EXECUTE FUNCTION urmail_mailbox_sieve_mode_guard();
