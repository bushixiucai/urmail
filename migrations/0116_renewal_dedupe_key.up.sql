-- BILL-003: idempotent settlement of recurring renewals.
-- The dedupe key (subscription_id:period_end:attempt_id) is the idempotency
-- key echoed to a payment provider when a renewal attempt is charged, so an
-- uncertain outcome can be resolved without ever charging twice.
ALTER TABLE subscription_renewal_attempts
    ADD COLUMN dedupe_key TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX subscription_renewal_attempts_dedupe_key_uniq
    ON subscription_renewal_attempts (dedupe_key)
    WHERE dedupe_key <> '';
