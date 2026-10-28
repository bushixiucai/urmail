-- Refuse to discard hosted provider or authorization facts on downgrade.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM recurring_setup_intents) THEN
  RAISE EXCEPTION 'recurring setup facts must be exported and explicitly resolved before downgrade';
 END IF;
END $$;
DROP TABLE recurring_setup_intents;
