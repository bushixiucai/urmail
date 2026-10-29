-- A frozen quota timezone is durable configuration history, including after
-- disabling caps. Never silently discard that configuration or reset usage.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM ai_provider_configs WHERE nonplatform_call_limit<>0 OR nonplatform_mailbox_call_limit<>0 OR nonplatform_concurrency_limit<>0 OR quota_timezone<>'') THEN
  RAISE EXCEPTION 'configured nonplatform AI quota facts prevent downgrade';
 END IF;
END $$;
DROP INDEX ai_usage_active_reservations_idx;
DROP INDEX ai_usage_nonplatform_window_idx;
DROP TRIGGER nonplatform_ai_quota_guard ON ai_provider_configs;
DROP FUNCTION urmail_guard_nonplatform_ai_quota();
ALTER TABLE ai_provider_configs DROP CONSTRAINT nonplatform_ai_quota_complete,
 DROP COLUMN nonplatform_call_limit,DROP COLUMN nonplatform_mailbox_call_limit,
 DROP COLUMN nonplatform_concurrency_limit,DROP COLUMN quota_timezone;
