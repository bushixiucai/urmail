-- Recurring debit is a separate, explicit authorization. Existing paid
-- subscriptions and merchant checkout credentials never imply enrollment.
DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='subscriptions_tenant_id_id_unique' AND conrelid='subscriptions'::regclass) THEN ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_tenant_id_id_unique UNIQUE(tenant_id,id); END IF; END $$;
CREATE TABLE IF NOT EXISTS payment_recurring_capabilities (
 merchant_id bigint PRIMARY KEY REFERENCES payment_merchants(id),
 adapter_key text NOT NULL DEFAULT '',
 merchant_approved boolean NOT NULL DEFAULT false,
 runtime_available boolean NOT NULL DEFAULT false,
 evidence_at timestamptz,
 evidence_expires_at timestamptz,
 evidence_kind text NOT NULL DEFAULT '' CHECK(evidence_kind IN('','sandbox','live')),
 merchant_config_at timestamptz,
 evidence_reference text NOT NULL DEFAULT '' CHECK(length(evidence_reference)<=500),
 updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE payment_recurring_capabilities OWNER TO urmail_platform;
ALTER TABLE payment_recurring_capabilities ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON payment_recurring_capabilities FROM PUBLIC,urmail_app;

CREATE TABLE IF NOT EXISTS subscription_renewal_authorizations (
 id bigserial PRIMARY KEY,
 tenant_id bigint NOT NULL,
 subscription_id bigint NOT NULL,
 merchant_id bigint NOT NULL REFERENCES payment_merchants(id),
 authorization_ciphertext text NOT NULL CHECK(length(authorization_ciphertext) BETWEEN 1 AND 8192),
 authorization_hash text NOT NULL CHECK(length(authorization_hash)=64),
 label text NOT NULL CHECK(length(label)<=200),
 currency text NOT NULL CHECK(currency IN('CNY','USD')),
 verified_at timestamptz NOT NULL,
 expires_at timestamptz,
 revoked_at timestamptz,
 merchant_config_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,subscription_id) REFERENCES subscriptions(tenant_id,id),
 UNIQUE(merchant_id,authorization_hash),
 UNIQUE(tenant_id,id)
);
CREATE TABLE IF NOT EXISTS subscription_renewal_enrollments (
 id bigserial PRIMARY KEY,
 tenant_id bigint NOT NULL,
 subscription_id bigint NOT NULL,
 binding_id bigint NOT NULL,
 state text NOT NULL CHECK(state IN('active','revoked','needs_reauthorization','needs_reconsent')),
 version bigint NOT NULL DEFAULT 1,
 consent_version text NOT NULL,
 consented_by bigint NOT NULL REFERENCES users(id),
 consented_at timestamptz NOT NULL DEFAULT now(),
 revoked_at timestamptz,
 consent_quote_digest text NOT NULL CHECK(length(consent_quote_digest)=64),
 amount_cents bigint NOT NULL CHECK(amount_cents>0),
 currency text NOT NULL CHECK(currency IN('CNY','USD')),
 billing_cycle text NOT NULL CHECK(billing_cycle IN('monthly','quarterly','annual')),
 plan_id text NOT NULL,
 plan_snapshot jsonb NOT NULL,
 plan_snapshot_digest text NOT NULL CHECK(length(plan_snapshot_digest)=64),
 expected_period_end timestamptz NOT NULL,
 renewal_period_end timestamptz NOT NULL,
 calendar_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,subscription_id) REFERENCES subscriptions(tenant_id,id),
 FOREIGN KEY(tenant_id,binding_id) REFERENCES subscription_renewal_authorizations(tenant_id,id),
 UNIQUE(tenant_id,id)
);
CREATE UNIQUE INDEX IF NOT EXISTS subscription_one_active_renewal ON subscription_renewal_enrollments(subscription_id) WHERE state='active';
ALTER TABLE subscription_renewal_authorizations OWNER TO urmail_platform;
ALTER TABLE subscription_renewal_enrollments OWNER TO urmail_platform;
ALTER TABLE subscription_renewal_authorizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscription_renewal_authorizations FORCE ROW LEVEL SECURITY;
ALTER TABLE subscription_renewal_enrollments ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscription_renewal_enrollments FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS renewal_authorization_tenant ON subscription_renewal_authorizations;
CREATE POLICY renewal_authorization_tenant ON subscription_renewal_authorizations USING(tenant_id=NULLIF(current_setting('urmail.tenant_id',true),'')::bigint) WITH CHECK(tenant_id=NULLIF(current_setting('urmail.tenant_id',true),'')::bigint);
DROP POLICY IF EXISTS renewal_enrollment_tenant ON subscription_renewal_enrollments;
CREATE POLICY renewal_enrollment_tenant ON subscription_renewal_enrollments USING(tenant_id=NULLIF(current_setting('urmail.tenant_id',true),'')::bigint) WITH CHECK(tenant_id=NULLIF(current_setting('urmail.tenant_id',true),'')::bigint);
REVOKE ALL ON subscription_renewal_authorizations,subscription_renewal_enrollments FROM PUBLIC,urmail_app;

ALTER TABLE subscription_renewal_attempts DROP CONSTRAINT subscription_renewal_attempts_state_check;
ALTER TABLE subscription_renewal_attempts ADD CONSTRAINT subscription_renewal_attempts_state_check CHECK(state IN('pending','preparing','charging','uncertain','reconciling','failed','succeeded','canceled'));
ALTER TABLE subscription_renewal_attempts
 ADD COLUMN enrollment_id bigint,
 ADD COLUMN binding_id bigint,
 ADD COLUMN merchant_id bigint REFERENCES payment_merchants(id),
 ADD COLUMN merchant_scope text NOT NULL DEFAULT '',
 ADD COLUMN amount_cents bigint CHECK(amount_cents>0),
 ADD COLUMN currency text,
 ADD COLUMN plan_snapshot jsonb,
 ADD COLUMN plan_snapshot_digest text NOT NULL DEFAULT '',
 ADD COLUMN billing_cycle text,
 ADD COLUMN claim_token bigint NOT NULL DEFAULT 0,
 ADD COLUMN charge_count integer NOT NULL DEFAULT 0,
 ADD COLUMN charge_history jsonb NOT NULL DEFAULT '[]'::jsonb,
 ADD COLUMN lease_until timestamptz,
 ADD COLUMN side_effect_intent_at timestamptz,
 ADD COLUMN provider_request_id text NOT NULL DEFAULT '',
 ADD COLUMN renewal_period_end timestamptz,
 ADD COLUMN calendar_snapshot jsonb,
 ADD FOREIGN KEY(tenant_id,enrollment_id) REFERENCES subscription_renewal_enrollments(tenant_id,id),
 ADD FOREIGN KEY(tenant_id,binding_id) REFERENCES subscription_renewal_authorizations(tenant_id,id);
-- Unknown historical outcomes remain held. Never infer a new mandate or
-- erase a possible financial side effect while upgrading.
UPDATE subscription_renewal_attempts SET state='uncertain',side_effect_intent_at=updated_at,last_error='LEGACY_OUTCOME_REQUIRES_RECONCILIATION'
WHERE state='pending' AND attempts>0 AND last_error<>'NO_SAVED_PAYMENT_METHOD';
UPDATE subscription_renewal_attempts SET state='canceled',last_error='EXPLICIT_ENROLLMENT_REQUIRED',updated_at=now()
WHERE state='pending';

CREATE FUNCTION revoke_renewal_on_subscription_cancel() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.cancel_at_period_end AND NOT OLD.cancel_at_period_end THEN
  UPDATE public.subscription_renewal_enrollments SET state='revoked',version=version+1,revoked_at=now(),updated_at=now() WHERE tenant_id=NEW.tenant_id AND subscription_id=NEW.id AND state='active';
  UPDATE public.subscription_renewal_attempts SET state='canceled',last_error='ENROLLMENT_REVOKED',updated_at=now() WHERE tenant_id=NEW.tenant_id AND subscription_id=NEW.id AND state IN('pending','preparing') AND side_effect_intent_at IS NULL;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION revoke_renewal_on_subscription_cancel() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION revoke_renewal_on_subscription_cancel() FROM PUBLIC;
CREATE TRIGGER subscription_cancel_revokes_renewal AFTER UPDATE OF cancel_at_period_end ON subscriptions FOR EACH ROW EXECUTE FUNCTION revoke_renewal_on_subscription_cancel();
