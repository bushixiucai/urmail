ALTER TABLE coupons ADD COLUMN bound_tenant_id bigint REFERENCES tenants(id),
 ADD COLUMN source_coupon_code text,
 ADD COLUMN source_tenant_id bigint,
 ADD COLUMN reissue_reason text,
 ADD COLUMN reissued_by bigint REFERENCES users(id),
 ADD COLUMN reissued_at timestamptz,
 ADD CONSTRAINT coupon_reissue_source_fk FOREIGN KEY(source_coupon_code,source_tenant_id) REFERENCES coupon_redemptions(coupon_code,tenant_id),
 ADD CONSTRAINT coupon_reissue_fact_check CHECK (
  (source_coupon_code IS NULL AND source_tenant_id IS NULL AND reissue_reason IS NULL AND reissued_by IS NULL AND reissued_at IS NULL AND bound_tenant_id IS NULL)
  OR (source_coupon_code IS NOT NULL AND source_tenant_id IS NOT NULL AND bound_tenant_id IS NOT NULL AND bound_tenant_id=source_tenant_id AND reissue_reason IS NOT NULL AND char_length(trim(reissue_reason)) BETWEEN 10 AND 1000 AND reissued_by IS NOT NULL AND reissued_at IS NOT NULL AND max_redemptions=1 AND valid_to IS NOT NULL AND valid_from IS NOT NULL AND isfinite(valid_to) AND valid_to>valid_from));
CREATE UNIQUE INDEX coupon_reissue_once ON coupons(source_coupon_code,source_tenant_id) WHERE source_coupon_code IS NOT NULL;
REVOKE INSERT,UPDATE,DELETE ON coupons FROM urmail_app;
ALTER TABLE coupons ENABLE ROW LEVEL SECURITY;
CREATE POLICY coupon_tenant_scope ON coupons TO urmail_app USING(bound_tenant_id IS NULL OR bound_tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
CREATE FUNCTION urmail_coupon_bound_redemption() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM coupons WHERE code=NEW.coupon_code AND bound_tenant_id IS NOT NULL AND bound_tenant_id<>NEW.tenant_id) THEN RAISE EXCEPTION 'COUPON_TENANT_MISMATCH' USING ERRCODE='42501'; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_coupon_bound_redemption() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_coupon_bound_redemption() FROM PUBLIC;
CREATE TRIGGER coupon_redemptions_bound_guard BEFORE INSERT ON coupon_redemptions FOR EACH ROW EXECUTE FUNCTION urmail_coupon_bound_redemption();
CREATE FUNCTION urmail_coupon_reissue_immutable() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.source_coupon_code IS NOT NULL THEN RAISE EXCEPTION 'COUPON_REISSUE_IMMUTABLE' USING ERRCODE='23514'; END IF;
  RETURN OLD;
 END IF;
 IF OLD.source_coupon_code IS NOT NULL AND (NEW.code,NEW.kind,NEW.value,NEW.max_redemptions,NEW.currency,NEW.valid_from,NEW.valid_to,NEW.bound_tenant_id,NEW.source_coupon_code,NEW.source_tenant_id,NEW.reissue_reason,NEW.reissued_by,NEW.reissued_at) IS DISTINCT FROM (OLD.code,OLD.kind,OLD.value,OLD.max_redemptions,OLD.currency,OLD.valid_from,OLD.valid_to,OLD.bound_tenant_id,OLD.source_coupon_code,OLD.source_tenant_id,OLD.reissue_reason,OLD.reissued_by,OLD.reissued_at) THEN RAISE EXCEPTION 'COUPON_REISSUE_IMMUTABLE' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_coupon_reissue_immutable() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_coupon_reissue_immutable() FROM PUBLIC;
CREATE TRIGGER coupon_reissue_immutable BEFORE UPDATE OR DELETE ON coupons FOR EACH ROW EXECUTE FUNCTION urmail_coupon_reissue_immutable();
