-- Never discard retained recovery material during a schema rollback.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM mailbox_scheduled_sends WHERE sent_raw_secret<>'' OR (status='sent' AND sent_copy_state IN('pending','unknown','held'))) THEN
  RAISE EXCEPTION 'SCHEDULED_RECOVERY_REQUIRES_REVIEW_BEFORE_DOWNGRADE';
 END IF;
END $$;
ALTER TABLE mailbox_scheduled_sends DROP COLUMN sent_raw_secret, DROP COLUMN copy_pending_since, DROP COLUMN copy_attempts, DROP COLUMN smtp_password_version;
