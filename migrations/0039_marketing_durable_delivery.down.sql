DROP TRIGGER IF EXISTS marketing_campaign_cancel ON marketing_campaigns;
DROP FUNCTION IF EXISTS urmail_cancel_campaign_tasks();
DROP FUNCTION urmail_record_marketing_feedback(bigint,text,bigint,text,text);
DROP FUNCTION urmail_resolve_marketing_attempt(bigint,bigint,text,text);
DROP FUNCTION urmail_finish_marketing_send(bigint,bigint,text,bigint,text);
DROP FUNCTION urmail_authorize_marketing_send(bigint,bigint,text,bigint);
DROP FUNCTION urmail_schedule_marketing(bigint,bigint,timestamptz);
DROP TABLE marketing_feedback,marketing_recipients,marketing_quota_windows,marketing_campaigns,marketing_contacts,marketing_lists,marketing_templates,marketing_channels;
DROP FUNCTION urmail_require_marketing_admin();
