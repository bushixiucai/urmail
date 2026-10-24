-- A tenant-writable order is an intention, never proof of authenticated pricing.
CREATE TABLE billing_order_authorizations (
 order_id bigint PRIMARY KEY,
 tenant_id bigint NOT NULL,
 authority_digest text NOT NULL CHECK(authority_digest ~ '^[a-f0-9]{64}$'),
 authorized_by bigint REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,order_id) REFERENCES orders(tenant_id,id)
);
ALTER TABLE billing_order_authorizations OWNER TO urmail_platform;
REVOKE ALL ON billing_order_authorizations FROM PUBLIC,urmail_app,urmail_dovecot,urmail_roundcube;
ALTER TABLE billing_order_authorizations ENABLE ROW LEVEL SECURITY;
CREATE POLICY deny_application_access ON billing_order_authorizations USING(false);
-- Intent rows cannot be promoted to receipts or rewritten by the application role.
CREATE FUNCTION urmail_order_intention_guard() RETURNS trigger LANGUAGE plpgsql
SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF current_setting('role',true)='urmail_app' THEN
  IF TG_OP='INSERT' AND NEW.status<>'pending' THEN RAISE EXCEPTION 'ORDER_INTENTION_INVALID' USING ERRCODE='23514'; END IF;
  IF TG_OP='UPDATE' AND ((to_jsonb(NEW)-'status'-'quote_digest') IS DISTINCT FROM (to_jsonb(OLD)-'status'-'quote_digest') OR (NEW.quote_digest IS DISTINCT FROM OLD.quote_digest AND NOT(OLD.quote_digest IS NULL AND NEW.quote_digest IS NOT NULL AND OLD.status='pending' AND NEW.status='pending')) OR (NEW.status IS DISTINCT FROM OLD.status AND NOT(OLD.status='pending' AND NEW.status='canceled'))) THEN RAISE EXCEPTION 'ORDER_INTENTION_IMMUTABLE' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_order_intention_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_order_intention_guard() FROM PUBLIC;
CREATE TRIGGER order_intention_guard BEFORE INSERT OR UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION urmail_order_intention_guard();
REVOKE DELETE ON orders FROM urmail_app;
REVOKE UPDATE ON coupon_redemptions FROM urmail_app;
CREATE FUNCTION urmail_coupon_release_authority() RETURNS trigger LANGUAGE plpgsql
SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE source_status text;
BEGIN
 IF OLD.order_id IS NULL THEN RAISE EXCEPTION 'COUPON_REDEMPTION_SETTLED' USING ERRCODE='CU003'; END IF;
 SELECT status INTO source_status FROM orders WHERE tenant_id=OLD.tenant_id AND id=OLD.order_id FOR UPDATE;
 IF source_status IS NULL OR source_status NOT IN('canceled','expired','failed') THEN RAISE EXCEPTION 'COUPON_REDEMPTION_SETTLED' USING ERRCODE='CU003'; END IF;
 RETURN OLD;
END $$;
ALTER FUNCTION urmail_coupon_release_authority() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_coupon_release_authority() FROM PUBLIC;
CREATE TRIGGER coupon_release_authority BEFORE DELETE ON coupon_redemptions FOR EACH ROW EXECUTE FUNCTION urmail_coupon_release_authority();
-- These are derived ledger facts, not tenant-authored intentions.
REVOKE INSERT,UPDATE,DELETE ON invoices,commissions,order_items FROM urmail_app;
REVOKE UPDATE,DELETE ON channel_attribution FROM urmail_app;
