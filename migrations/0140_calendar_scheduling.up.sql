-- Scheduling identity belongs to the event; an incoming message cannot replace
-- an existing organizer merely by reusing its UID.
ALTER TABLE mailbox_events
 ADD COLUMN organizer text NOT NULL DEFAULT '' CHECK(length(organizer)<=320 AND organizer!~E'[\r\n]'),
 ADD COLUMN invitation_sequence integer NOT NULL DEFAULT 0 CHECK(invitation_sequence>=0),
 ADD COLUMN invitation_stamp timestamptz,
 ADD COLUMN invitation_digest text NOT NULL DEFAULT '' CHECK(length(invitation_digest) IN(0,64)),
 ADD COLUMN invitation_response text NOT NULL DEFAULT '' CHECK(invitation_response IN('','needs-action','accepted','declined','tentative')),
 ADD COLUMN reminders jsonb NOT NULL DEFAULT '[]'::jsonb CHECK(jsonb_typeof(reminders)='array' AND jsonb_array_length(reminders)<=5),
 ADD COLUMN reminder_scanned_at timestamptz NOT NULL DEFAULT '1970-01-01T00:00:00Z';
CREATE INDEX mailbox_event_reminder_scan ON mailbox_events(reminder_scanned_at,id) WHERE deleted_at IS NULL AND reminders<>'[]'::jsonb;
CREATE INDEX mailbox_event_scheduling_identity ON mailbox_events(tenant_id,mailbox_id,uid,organizer) WHERE organizer<>'';
