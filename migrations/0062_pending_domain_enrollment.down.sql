DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'/* pending_manual_enrollment */ EXISTS(SELECT 1 FROM tenants t WHERE t.id=tid AND t.status=''pending'' AND (EXISTS(SELECT 1 FROM tenant_contacts tc WHERE tc.tenant_id=tid) OR (t.owner_user_id=nullif(current_setting(''urmail.user_id'',true),'''')::bigint AND EXISTS(SELECT 1 FROM user_tenant_roles r WHERE r.tenant_id=tid AND r.user_id=t.owner_user_id AND r.role=''owner''))))',
 'EXISTS(SELECT 1 FROM tenant_contacts tc JOIN tenants t ON t.id=tc.tenant_id WHERE tc.tenant_id=tid AND t.status=''pending'')');
 EXECUTE definition;
END $$;
