-- Reviewed legacy metadata must be staged without claiming DNS/provisioning readiness.
-- Only the privileged operator role may allocate into unready legacy domains;
-- tenant requests and all mail/authentication consumers retain the readiness gate.
DO $$ DECLARE definition text; patched text; BEGIN
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 patched:=replace(definition,
 'IF NOT urmail_domain_mail_ready(tid,NEW.domain_id) THEN RAISE EXCEPTION ''DOMAIN_NOT_PROVISIONED'' USING ERRCODE=''23514'';END IF;',
 'IF NOT urmail_domain_mail_ready(tid,NEW.domain_id) AND NOT (
  /* reviewed_legacy_metadata_only */
  (current_setting(''role'',true) IN(''urmail_platform'',''urmail_migrate'')
   OR (current_setting(''role'',true)=''none'' AND pg_has_role(session_user,''urmail_platform'',''USAGE'')))
  AND urmail_migration_entitlement_allowed(tid)
 ) THEN RAISE EXCEPTION ''DOMAIN_NOT_PROVISIONED'' USING ERRCODE=''23514'';END IF;');
 IF patched=definition THEN RAISE EXCEPTION 'LEGACY_METADATA_GATE_UPDATE_REQUIRED';END IF;
 EXECUTE patched;
END $$;
