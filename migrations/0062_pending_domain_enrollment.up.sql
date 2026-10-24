-- Operator-created pending spaces can prove one domain without a mail gift.
-- Self-service spaces still cannot exist before contact-email verification.
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'EXISTS(SELECT 1 FROM tenant_contacts tc JOIN tenants t ON t.id=tc.tenant_id WHERE tc.tenant_id=tid AND t.status=''pending'')',
 '/* pending_manual_enrollment */ EXISTS(SELECT 1 FROM tenants t WHERE t.id=tid AND t.status=''pending'' AND (EXISTS(SELECT 1 FROM tenant_contacts tc WHERE tc.tenant_id=tid) OR (t.owner_user_id=nullif(current_setting(''urmail.user_id'',true),'''')::bigint AND EXISTS(SELECT 1 FROM user_tenant_roles r WHERE r.tenant_id=tid AND r.user_id=t.owner_user_id AND r.role=''owner''))))');
 IF strpos(definition,'pending_manual_enrollment')=0 THEN RAISE EXCEPTION 'PENDING_ENROLLMENT_GATE_UPDATE_REQUIRED'; END IF;
 EXECUTE definition;
END $$;
