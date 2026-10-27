-- Attendees belong to the original event.
-- Storing them does not send an invitation.

ALTER TABLE mailbox_events ADD COLUMN attendees jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE mailbox_events ADD CONSTRAINT mailbox_events_attendees_size CHECK(jsonb_typeof(attendees)='array' AND jsonb_array_length(attendees)<=20);
