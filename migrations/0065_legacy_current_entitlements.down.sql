DO $$ DECLARE definition text; BEGIN
 IF EXISTS(SELECT 1 FROM tenants t WHERE urmail_legacy_current_entitlement_end(t.id)>now()) THEN
  RAISE EXCEPTION 'LEGACY_CURRENT_RIGHTS_REQUIRE_CONTROLLED_ROLLBACK' USING ERRCODE='23514';
 END IF;
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 EXECUTE replace(definition,'/* legacy_current_gate */ COALESCE(urmail_legacy_current_entitlement_end(s.tenant_id)>now(),false) OR ','');
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 EXECUTE replace(definition,'NOT COALESCE(urmail_legacy_current_entitlement_end(tid)>now(),false) AND /* legacy_current_gate */ ','');
END $$;
DROP FUNCTION urmail_legacy_current_entitlement_end(bigint);
