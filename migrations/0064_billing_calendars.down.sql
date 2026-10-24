DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM billing_calendars) OR EXISTS(SELECT 1 FROM subscription_terms WHERE calendar_snapshot IS NOT NULL) THEN
  RAISE EXCEPTION 'BILLING_CALENDAR_REQUIRES_CONTROLLED_ROLLBACK' USING ERRCODE='23514';
 END IF;
END $$;
ALTER TABLE subscription_terms DROP COLUMN calendar_snapshot;
DROP TABLE billing_calendars;
DROP FUNCTION urmail_billing_calendar_guard();

DO $$
DECLARE signature regprocedure;
BEGIN
 FOREACH signature IN ARRAY ARRAY['urmail_quota_day(bigint)'::regprocedure,'urmail_authorize_marketing_send(bigint,bigint,text,bigint)'::regprocedure] LOOP
  EXECUTE replace(pg_get_functiondef(signature),'urmail_business_timezone(p_tenant)','COALESCE((SELECT value#>>''{}'' FROM settings WHERE scope=''tenant'' AND scope_id=p_tenant AND key=''business_timezone''),''Asia/Shanghai'')');
 END LOOP;
END $$;
DROP FUNCTION urmail_business_timezone(bigint);

DROP FUNCTION urmail_billing_history_requires_review(bigint,bigint);
