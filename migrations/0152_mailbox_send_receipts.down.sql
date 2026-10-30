-- Receipt identities cannot be discarded while callers may replay their keys.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM mailbox_send_receipts) THEN
  RAISE EXCEPTION 'SEND_RECEIPTS_REQUIRE_REVIEW_BEFORE_DOWNGRADE';
 END IF;
END $$;
DROP TABLE mailbox_send_receipts;
