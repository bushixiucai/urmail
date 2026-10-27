-- OPS-103: the email delivery state machine records why a notification is
-- retrying or poisoned, so last delivery errors are observable per message.
ALTER TABLE notification_outbox ADD COLUMN last_error text;
