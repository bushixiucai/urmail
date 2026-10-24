-- Remove only this migration's pending-domain exception.
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'IF TG_TABLE_NAME=''domains'' AND TG_OP=''INSERT'' AND EXISTS(SELECT 1 FROM tenant_contacts tc JOIN tenants t ON t.id=tc.tenant_id WHERE tc.tenant_id=tid AND t.status=''pending'') AND NOT EXISTS(SELECT 1 FROM subscriptions WHERE tenant_id=tid AND status IN(''active'',''trialing'',''past_due'')) THEN
 IF (SELECT count(*) FROM domains WHERE tenant_id=tid)>=1 THEN RAISE EXCEPTION ''DOMAIN_QUOTA_EXCEEDED'' USING ERRCODE=''23514''; END IF; RETURN NEW; END IF;
 ', '');
 EXECUTE definition;
END $$;
ALTER TABLE notification_outbox DROP COLUMN secret_payload;
ALTER TABLE notification_outbox DROP CONSTRAINT notification_outbox_check;
ALTER TABLE notification_outbox ADD CHECK(channel<>'email' OR (recipient IS NOT NULL AND recipient_verified));
DROP TABLE tenant_contacts;
DROP TABLE registration_requests;
