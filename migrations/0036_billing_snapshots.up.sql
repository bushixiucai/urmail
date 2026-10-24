-- Persist confirmed prices and isolate paid entitlements from editable plans.
CREATE TABLE plan_prices (
    plan_code text NOT NULL REFERENCES plans(code) ON DELETE CASCADE,
    currency text NOT NULL CHECK (currency IN ('CNY','USD')),
    billing_cycle text NOT NULL CHECK (billing_cycle IN ('monthly','quarterly','annual')),
    amount_cents bigint NOT NULL CHECK (amount_cents BETWEEN 0 AND 9007199254740991),
    version bigint GENERATED ALWAYS AS IDENTITY NOT NULL UNIQUE,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(plan_code,currency,billing_cycle)
);
ALTER TABLE plan_prices OWNER TO urmail_platform;
REVOKE ALL ON plan_prices FROM PUBLIC,urmail_app,urmail_dovecot;
GRANT SELECT ON plan_prices TO urmail_app;
INSERT INTO plan_prices(plan_code,currency,billing_cycle,amount_cents)
SELECT code,currency,'monthly',price_cents FROM plans
WHERE currency IN ('CNY','USD') AND price_cents BETWEEN 0 AND 9007199254740991;

CREATE TABLE billing_quotes (
    digest text PRIMARY KEY CHECK (digest ~ '^[a-f0-9]{64}$'),
    tenant_id bigint NOT NULL REFERENCES tenants(id),
    payload jsonb NOT NULL,
    plan_snapshot jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(tenant_id,digest)
);
ALTER TABLE billing_quotes OWNER TO urmail_platform;
REVOKE ALL ON billing_quotes FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE billing_quotes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON billing_quotes
USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint)
WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
GRANT SELECT,INSERT ON billing_quotes TO urmail_app;
ALTER TABLE orders ADD COLUMN quote_digest text;
ALTER TABLE orders ADD CONSTRAINT orders_quote_tenant_fk FOREIGN KEY(tenant_id,quote_digest) REFERENCES billing_quotes(tenant_id,digest);
ALTER TABLE subscriptions ADD COLUMN plan_snapshot jsonb;
-- Existing rows retain their current, observable plan; this is not a claim
-- that an earlier historical snapshot can be reconstructed.
UPDATE subscriptions s SET plan_snapshot=to_jsonb(p) FROM plans p WHERE p.code=s.plan_id;

CREATE TABLE subscription_terms (
 order_id bigint PRIMARY KEY REFERENCES orders(id),
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 period_start timestamptz NOT NULL,
 billing_period_start timestamptz NOT NULL,
 period_end timestamptz NOT NULL CHECK(period_end>period_start),
 plan_snapshot jsonb NOT NULL,
 base_amount_cents bigint NOT NULL CHECK(base_amount_cents>=0),
 currency text NOT NULL,
 billing_cycle text NOT NULL,
 FOREIGN KEY(tenant_id,order_id) REFERENCES orders(tenant_id,id)
);
ALTER TABLE subscription_terms OWNER TO urmail_platform;
REVOKE ALL ON subscription_terms FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE subscription_terms ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON subscription_terms
USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
GRANT SELECT ON subscription_terms TO urmail_app;
CREATE INDEX ON subscription_terms(tenant_id,period_start,period_end);

CREATE FUNCTION urmail_effective_plan(p_tenant bigint) RETURNS SETOF plans
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT (jsonb_populate_record(NULL::plans,COALESCE((SELECT term.plan_snapshot FROM subscription_terms term WHERE term.tenant_id=s.tenant_id AND term.period_start<=now() AND term.period_end>now() ORDER BY term.period_start DESC,term.order_id DESC LIMIT 1),s.plan_snapshot,to_jsonb(p)))).*
 FROM subscriptions s JOIN plans p ON p.code=s.plan_id
 WHERE s.tenant_id=p_tenant AND (p_tenant=nullif(current_setting('urmail.tenant_id',true),'')::bigint OR current_setting('role',true) IN ('urmail_platform','urmail_dovecot','urmail_migrate')) AND s.status IN('active','trialing','past_due')
 LIMIT 1
$$;
ALTER FUNCTION urmail_effective_plan(bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_effective_plan(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_effective_plan(bigint) TO urmail_app,urmail_dovecot;

-- Preserve all existing resource and subscription guards, replacing only
-- their plan lookup with the frozen entitlement record.
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'SELECT * INTO p FROM plans WHERE code=s.plan_id;',
     'SELECT * INTO p FROM urmail_effective_plan(tid);');
 EXECUTE definition;
END $$;
CREATE FUNCTION urmail_freeze_subscription_plan() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF current_setting('role',true) NOT IN ('urmail_platform','urmail_migrate') THEN
  IF TG_OP='UPDATE' AND NEW.plan_snapshot IS DISTINCT FROM OLD.plan_snapshot THEN
   RAISE EXCEPTION 'ENTITLEMENT_SNAPSHOT_IMMUTABLE' USING ERRCODE='42501';
  END IF;
  IF TG_OP='INSERT' THEN NEW.plan_snapshot:=NULL; END IF;
 END IF;
 IF NEW.plan_snapshot IS NULL OR (TG_OP='UPDATE' AND NEW.plan_id IS DISTINCT FROM OLD.plan_id) THEN
  SELECT to_jsonb(p) INTO NEW.plan_snapshot FROM plans p WHERE code=NEW.plan_id;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_freeze_subscription_plan() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_freeze_subscription_plan() FROM PUBLIC;
CREATE TRIGGER a_freeze_subscription_plan BEFORE INSERT OR UPDATE ON subscriptions
FOR EACH ROW EXECUTE FUNCTION urmail_freeze_subscription_plan();
-- Use the proposed row snapshot when checking a subscription replacement.
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_subscription_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'SELECT * INTO p FROM plans WHERE code=NEW.plan_id;',
  'SELECT * INTO p FROM jsonb_populate_record(NULL::plans,NEW.plan_snapshot);');
 EXECUTE definition;
END $$;
ALTER TABLE coupons ADD COLUMN currency text CHECK(currency IS NULL OR currency IN('CNY','USD'));

-- Adjustments explicitly reference their original paid basis rather than
-- representing a second overlapping base billing period.
ALTER TABLE subscription_terms ADD CONSTRAINT subscription_term_tenant_order_unique UNIQUE(tenant_id,order_id);
ALTER TABLE subscription_terms ADD COLUMN basis_order_id bigint;
ALTER TABLE subscription_terms ADD CONSTRAINT subscription_term_basis_fk
 FOREIGN KEY(tenant_id,basis_order_id) REFERENCES subscription_terms(tenant_id,order_id);
CREATE FUNCTION urmail_subscription_term_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE basis subscription_terms%ROWTYPE;
BEGIN
 PERFORM pg_advisory_xact_lock(NEW.tenant_id);
 IF NEW.basis_order_id IS NULL THEN
  IF EXISTS(SELECT 1 FROM subscription_terms t WHERE t.tenant_id=NEW.tenant_id
   AND t.basis_order_id IS NULL AND t.period_start<NEW.period_end AND t.period_end>NEW.period_start) THEN
   RAISE EXCEPTION 'BILLING_PERIOD_OVERLAP' USING ERRCODE='23514';
  END IF;
 ELSE
  SELECT * INTO basis FROM subscription_terms WHERE tenant_id=NEW.tenant_id AND order_id=NEW.basis_order_id;
  IF NOT FOUND OR basis.basis_order_id IS NOT NULL OR NEW.billing_period_start<>basis.billing_period_start
   OR NEW.period_end<>basis.period_end OR NEW.period_start<basis.period_start
   OR NEW.currency<>basis.currency OR NEW.billing_cycle<>basis.billing_cycle THEN
   RAISE EXCEPTION 'BILLING_ADJUSTMENT_BASIS_INVALID' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_subscription_term_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_subscription_term_guard() FROM PUBLIC;
CREATE TRIGGER subscription_term_guard BEFORE INSERT ON subscription_terms FOR EACH ROW EXECUTE FUNCTION urmail_subscription_term_guard();

-- Paid entitlements are changed only through verified platform settlement.
REVOKE INSERT,UPDATE,DELETE ON subscriptions FROM urmail_app;
GRANT UPDATE(cancel_at_period_end) ON subscriptions TO urmail_app;
