-- Persist bounded Ping configuration on the existing mailbox-owned device.
ALTER TABLE mailbox_eas_devices
 ADD COLUMN ping_heartbeat integer NOT NULL DEFAULT 0 CHECK(ping_heartbeat BETWEEN 0 AND 3600),
 ADD COLUMN ping_folders jsonb NOT NULL DEFAULT '[]'::jsonb CHECK(jsonb_typeof(ping_folders)='array' AND jsonb_array_length(ping_folders)<=100);
