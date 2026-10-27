ALTER TABLE mailbox_events DROP CONSTRAINT IF EXISTS mailbox_events_attendees_size;
ALTER TABLE mailbox_events DROP COLUMN IF EXISTS attendees;
