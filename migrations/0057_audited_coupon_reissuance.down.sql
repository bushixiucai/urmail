DROP TRIGGER coupon_reissue_immutable ON coupons;
DROP FUNCTION urmail_coupon_reissue_immutable();
DROP TRIGGER coupon_redemptions_bound_guard ON coupon_redemptions;
DROP FUNCTION urmail_coupon_bound_redemption();
DROP POLICY coupon_tenant_scope ON coupons;
ALTER TABLE coupons DISABLE ROW LEVEL SECURITY;
GRANT INSERT,UPDATE,DELETE ON coupons TO urmail_app;
DROP INDEX coupon_reissue_once;
ALTER TABLE coupons DROP CONSTRAINT coupon_reissue_source_fk,DROP CONSTRAINT coupon_reissue_fact_check,
 DROP COLUMN bound_tenant_id,DROP COLUMN source_coupon_code,DROP COLUMN source_tenant_id,DROP COLUMN reissue_reason,DROP COLUMN reissued_by,DROP COLUMN reissued_at;
