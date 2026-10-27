DROP INDEX IF EXISTS subscription_renewal_attempts_dedupe_key_uniq;
ALTER TABLE subscription_renewal_attempts DROP COLUMN IF EXISTS dedupe_key;
