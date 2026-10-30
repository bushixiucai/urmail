ALTER TABLE mailbox_scheduled_sends ADD COLUMN sent_raw_secret text NOT NULL DEFAULT '', ADD COLUMN copy_pending_since timestamptz, ADD COLUMN copy_attempts integer NOT NULL DEFAULT 0, ADD COLUMN smtp_password_version text NOT NULL DEFAULT '';
UPDATE mailbox_scheduled_sends SET copy_pending_since=COALESCE(sent_at,updated_at) WHERE status='sent' AND sent_copy_state IN('pending','unknown','held');
-- Legacy plaintext copies require owner review and sealing before further recovery.
UPDATE mailbox_scheduled_sends SET sent_copy_state='held',smtp_secret='',lease_until=NULL,last_error='SENT_COPY_REVIEW_REQUIRED' WHERE sent_raw IS NOT NULL AND status='sent';
