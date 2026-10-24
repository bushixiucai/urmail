ALTER TABLE payment_checkout_intents ADD COLUMN restore_hold boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN payment_checkout_intents.restore_hold IS 'Restored side-effect uncertainty requires an explicit platform receipt review even after instance quarantine is released';
