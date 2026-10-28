-- Single-occurrence edits belong to the existing authoritative series row.
ALTER TABLE mailbox_events ADD COLUMN recurrence_overrides jsonb NOT NULL DEFAULT '[]'::jsonb
 CHECK(jsonb_typeof(recurrence_overrides)='array' AND jsonb_array_length(recurrence_overrides)<=400);
