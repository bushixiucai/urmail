-- Downgrade refuses to silently remove readiness protection for pending domains.
DO $$ BEGIN IF EXISTS(SELECT 1 FROM domains WHERE provisioning_state<>'ready') OR EXISTS(SELECT 1 FROM dns_change_plans WHERE status NOT IN('draft','canceled','rolled_back')) THEN RAISE EXCEPTION 'DNS_ONBOARDING_DOWNGRADE_REQUIRES_RECONCILIATION';END IF;END $$;
DROP TRIGGER enqueue_verified_domain ON domains;
DROP FUNCTION urmail_enqueue_verified_domain();
DROP TRIGGER domain_provisioning_guard ON domains;
DROP FUNCTION urmail_domain_provisioning_guard();
DO $$ DECLARE definition text;BEGIN
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 definition:=replace(definition,' AND /* onboarding_domain_gate */ (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=t.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=t.id AND gd.status=''active'' AND gd.provisioning_state=''ready''))','');EXECUTE definition;
END $$;
DROP FUNCTION urmail_abandon_dns_plan(bigint,uuid,text);
DROP FUNCTION urmail_review_dns_plan(bigint,uuid,text);
DROP FUNCTION urmail_rollback_dns_plan(bigint,uuid,text);
DROP FUNCTION urmail_request_onboarding(bigint,bigint,text);
DROP FUNCTION urmail_cancel_dns_plan(bigint,uuid,text);
DROP FUNCTION urmail_confirm_dns_plan(bigint,uuid,text);
DROP FUNCTION urmail_create_dns_plan(uuid,bigint,bigint,bigint,bigint,text,text,text,jsonb,jsonb,jsonb);
DO $$ DECLARE definition text;BEGIN
 SELECT pg_get_functiondef('urmail_take_mailbox_quota(bigint,bigint)'::regprocedure) INTO definition;
 definition:=replace(definition,' AND /* onboarding_mailbox_gate */ d.provisioning_state=''ready''','');EXECUTE definition;
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'IF NOT urmail_domain_mail_ready(tid,NEW.domain_id) THEN RAISE EXCEPTION ''DOMAIN_NOT_PROVISIONED'' USING ERRCODE=''23514'';END IF;','');EXECUTE definition;
END $$;
DROP FUNCTION urmail_domain_mail_ready(bigint,bigint);
DROP FUNCTION urmail_revoke_dns_credential(bigint,bigint);
DROP FUNCTION urmail_save_dns_credential(bigint,bigint,text,text,text,text);
DROP FUNCTION urmail_dns_actor(bigint,bigint);
DROP TABLE managed_dns_zones,dns_record_actions,dns_change_plans,dns_credentials;
DROP INDEX onboarding_due_idx;
ALTER TABLE onboarding_tasks DROP CONSTRAINT onboarding_tasks_step_check;
UPDATE onboarding_tasks SET step='domain_verification' WHERE step IN('bootstrap_config','configuration_apply','protocol_readiness');
ALTER TABLE onboarding_tasks ADD CONSTRAINT onboarding_tasks_step_check CHECK(step IN('domain_verification','dns_propagation','certificate_issue','mailbox_provision','ready'));
ALTER TABLE onboarding_tasks DROP COLUMN fence,DROP COLUMN next_attempt_at,DROP COLUMN desired_generation,DROP COLUMN last_error_code;
DO $$ DECLARE signature text;definition text;BEGIN
 FOREACH signature IN ARRAY ARRAY['urmail_schedule_marketing(bigint,bigint,timestamptz)','urmail_authorize_marketing_send(bigint,bigint,text,bigint)'] LOOP
 SELECT pg_get_functiondef(signature::regprocedure) INTO definition;
 definition:=replace(definition,' AND /* onboarding_marketing_sender_gate */ d.provisioning_state=''ready''','');EXECUTE definition;
 END LOOP;
END $$;
ALTER TABLE domains DROP COLUMN provisioning_state,DROP COLUMN dns_explicit,DROP COLUMN custom_web_enabled;
GRANT UPDATE ON domains TO urmail_app;
