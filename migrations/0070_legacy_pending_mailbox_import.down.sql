DO $$ DECLARE definition text; patched text; BEGIN
 IF EXISTS(SELECT 1 FROM mailboxes m WHERE urmail_migration_entitlement_allowed(m.tenant_id) AND NOT urmail_domain_mail_ready(m.tenant_id,m.domain_id)) THEN
  RAISE EXCEPTION 'LEGACY_PENDING_MAILBOXES_REQUIRE_CONTROLLED_ROLLBACK';
 END IF;
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 patched:=replace(definition,
 'IF NOT urmail_domain_mail_ready(tid,NEW.domain_id) AND NOT (
  /* reviewed_legacy_metadata_only */
  (current_setting(''role'',true) IN(''urmail_platform'',''urmail_migrate'')
   OR (current_setting(''role'',true)=''none'' AND pg_has_role(session_user,''urmail_platform'',''USAGE'')))
  AND urmail_migration_entitlement_allowed(tid)
 ) THEN RAISE EXCEPTION ''DOMAIN_NOT_PROVISIONED'' USING ERRCODE=''23514'';END IF;',
 'IF NOT urmail_domain_mail_ready(tid,NEW.domain_id) THEN RAISE EXCEPTION ''DOMAIN_NOT_PROVISIONED'' USING ERRCODE=''23514'';END IF;');
 IF patched=definition THEN RAISE EXCEPTION 'LEGACY_METADATA_GATE_ROLLBACK_REQUIRED';END IF;
 EXECUTE patched;
END $$;
