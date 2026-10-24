-- Restore lookup semantics before removing the snapshot column.
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'SELECT * INTO p FROM urmail_effective_plan(tid);','SELECT * INTO p FROM plans WHERE code=s.plan_id;');
 EXECUTE definition;
 SELECT pg_get_functiondef('urmail_subscription_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'SELECT * INTO p FROM jsonb_populate_record(NULL::plans,NEW.plan_snapshot);','SELECT * INTO p FROM plans WHERE code=NEW.plan_id;');
 EXECUTE definition;
END $$;
DROP TRIGGER a_freeze_subscription_plan ON subscriptions;
DROP FUNCTION urmail_freeze_subscription_plan();
DROP FUNCTION urmail_effective_plan(bigint);
ALTER TABLE subscriptions DROP COLUMN plan_snapshot;
ALTER TABLE orders DROP CONSTRAINT orders_quote_tenant_fk;
ALTER TABLE orders DROP COLUMN quote_digest;
ALTER TABLE coupons DROP COLUMN currency;
DROP TABLE subscription_terms;
DROP FUNCTION urmail_subscription_term_guard();
DROP TABLE billing_quotes;
DROP TABLE plan_prices;

GRANT INSERT,UPDATE,DELETE ON subscriptions TO urmail_app;
