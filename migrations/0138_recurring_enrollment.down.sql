-- Financial intent history cannot be discarded by schema rollback. Keep the
-- forward-compatible schema (and roll back only binaries) after any debit
-- intent has been recorded; reconcile or restore an explicit pre-charge backup.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM subscription_renewal_attempts WHERE side_effect_intent_at IS NOT NULL OR charge_history<>'[]'::jsonb) THEN
  RAISE EXCEPTION 'RECURRING_FINANCIAL_HISTORY_REQUIRES_FORWARD_SCHEMA';
 END IF;
END $$;
-- Never turn an uncertain debit into a retryable charge on downgrade.
UPDATE subscription_renewal_attempts SET state='failed',last_error='DOWNGRADE_HELD_RECONCILIATION' WHERE state IN('charging','uncertain','reconciling');
UPDATE subscription_renewal_attempts SET state='canceled',last_error='DOWNGRADE_REVOKED_ENROLLMENT' WHERE state IN('pending','preparing');
DROP TRIGGER subscription_cancel_revokes_renewal ON subscriptions;
DROP FUNCTION revoke_renewal_on_subscription_cancel();
ALTER TABLE subscription_renewal_attempts DROP CONSTRAINT subscription_renewal_attempts_state_check;
ALTER TABLE subscription_renewal_attempts ADD CONSTRAINT subscription_renewal_attempts_state_check CHECK(state IN('pending','failed','succeeded','canceled'));
ALTER TABLE subscription_renewal_attempts DROP COLUMN enrollment_id,DROP COLUMN binding_id,DROP COLUMN merchant_id,DROP COLUMN merchant_scope,DROP COLUMN amount_cents,DROP COLUMN currency,DROP COLUMN plan_snapshot,DROP COLUMN plan_snapshot_digest,DROP COLUMN billing_cycle,DROP COLUMN claim_token,DROP COLUMN charge_count,DROP COLUMN charge_history,DROP COLUMN lease_until,DROP COLUMN side_effect_intent_at,DROP COLUMN provider_request_id,DROP COLUMN renewal_period_end,DROP COLUMN calendar_snapshot;
-- Retain authorization/consent history for financial audit and upgrade recovery;
-- revoke it so a rollback/redeploy cannot revive a canceled mandate.
UPDATE subscription_renewal_enrollments SET state='revoked',revoked_at=COALESCE(revoked_at,now()),updated_at=now();
UPDATE subscription_renewal_authorizations SET revoked_at=COALESCE(revoked_at,now());
UPDATE payment_recurring_capabilities SET runtime_available=false,merchant_approved=false;
