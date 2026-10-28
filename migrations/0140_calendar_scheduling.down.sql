-- Old binaries cannot protect organizer identity or deliver persisted alarms.
-- Preserve forward schema after using these features instead of discarding
-- scheduling facts during a binary rollback.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM mailbox_events WHERE organizer<>'' OR reminders<>'[]'::jsonb) THEN
  RAISE EXCEPTION 'CALENDAR_SCHEDULING_HISTORY_REQUIRES_FORWARD_SCHEMA';
 END IF;
END $$;
DROP INDEX mailbox_event_reminder_scan;
DROP INDEX mailbox_event_scheduling_identity;
ALTER TABLE mailbox_events DROP COLUMN organizer,DROP COLUMN invitation_sequence,DROP COLUMN invitation_stamp,DROP COLUMN invitation_digest,DROP COLUMN invitation_response,DROP COLUMN reminders,DROP COLUMN reminder_scanned_at;
