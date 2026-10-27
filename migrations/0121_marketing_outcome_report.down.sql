DROP TRIGGER IF EXISTS marketing_feedback_bounce_unknown ON marketing_feedback;
DROP FUNCTION IF EXISTS urmail_skip_marketing_recipient(bigint,bigint,text,bigint,text);
DROP FUNCTION IF EXISTS urmail_mark_marketing_recipient_revoked(bigint,bigint,text,bigint);
DROP TRIGGER IF EXISTS marketing_campaign_stats_bump ON marketing_recipients;
DROP FUNCTION IF EXISTS urmail_bump_marketing_bounce_unknown();
DROP FUNCTION IF EXISTS urmail_bump_marketing_campaign_stats();
DROP TABLE IF EXISTS marketing_campaign_stats;
