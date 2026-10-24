DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM subscriptions WHERE entitlement_source='migration_legacy') THEN
  RAISE EXCEPTION 'MIGRATION_ENTITLEMENTS_REQUIRE_CONTROLLED_ROLLBACK' USING ERRCODE='23514';
 END IF;
END $$;
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 EXECUTE replace(definition,'(s.period_end>now() OR urmail_migration_entitlement_allowed(s.tenant_id))','s.period_end>now()');
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 EXECUTE replace(definition,'COALESCE(s.period_end<=now(),NOT urmail_migration_entitlement_allowed(tid))','s.period_end<=now()');
 SELECT pg_get_functiondef('urmail_billing_history_requires_review(bigint,bigint)'::regprocedure) INTO definition;
 EXECUTE replace(definition,' AND s.entitlement_source<>''migration_legacy''','');
 SELECT pg_get_functiondef('urmail_redeem_trial(bigint,bigint,text,text)'::regprocedure) INTO definition;
 EXECUTE replace(definition,' IF urmail_migration_entitlement_allowed(p_tenant) THEN RAISE EXCEPTION ''MIGRATION_ENTITLEMENT_EXISTS'' USING ERRCODE=''23514''; END IF;','');
 SELECT pg_get_functiondef('urmail_request_onboarding(bigint,bigint,text)'::regprocedure) INTO definition;
 EXECUTE replace(definition,'(period_end>now() OR urmail_migration_entitlement_allowed(p_tenant))','period_end>now()');
 SELECT pg_get_functiondef('urmail_subscription_change_guard()'::regprocedure) INTO definition;
 EXECUTE replace(definition,' IF NEW.status<>''canceled'' AND urmail_migration_entitlement_allowed(NEW.tenant_id) THEN RAISE EXCEPTION ''MIGRATION_ENTITLEMENT_PRESERVED'' USING ERRCODE=''23514''; END IF;','');
 SELECT pg_get_functiondef('urmail_enqueue_verified_domain()'::regprocedure) INTO definition;
 EXECUTE replace(definition,'(period_end>now() OR urmail_migration_entitlement_allowed(NEW.tenant_id))','period_end>now()');
END $$;
DROP FUNCTION urmail_grant_migration_entitlement(bigint,text,bigint,text);
DROP TRIGGER b_migration_subscription_guard ON subscriptions;
DROP FUNCTION urmail_migration_subscription_guard();
DROP FUNCTION urmail_migration_entitlement_allowed(bigint);
DROP TRIGGER migration_plan_guard ON plans;
DROP FUNCTION urmail_migration_plan_guard();
DELETE FROM plans WHERE code='migration_legacy';
DROP INDEX subscription_migration_manifest_unique;
ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_entitlement_source_check;
ALTER TABLE subscriptions ALTER COLUMN period_end SET NOT NULL;
ALTER TABLE subscriptions DROP COLUMN entitlement_source,DROP COLUMN migration_manifest_sha256;
