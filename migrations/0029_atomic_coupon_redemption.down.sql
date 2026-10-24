-- Migration 0029 down
DROP TRIGGER IF EXISTS coupon_redemptions_capacity ON coupon_redemptions;
DROP TRIGGER IF EXISTS coupon_redemptions_guard ON coupon_redemptions;
REVOKE EXECUTE ON FUNCTION urmail_redeem_coupon_for_tenant(text,bigint,bigint) FROM urmail_platform;
DROP FUNCTION urmail_redeem_coupon_for_tenant(text,bigint,bigint);
REVOKE EXECUTE ON FUNCTION urmail_redeem_coupon(text,bigint) FROM urmail_app;
DROP FUNCTION urmail_redeem_coupon(text,bigint);
-- The trigger functions depend on the (still live) coupon_redemptions
-- triggers, so the triggers are dropped above before the functions; the
-- functions would otherwise survive and block a re-up (round-trip test).
DROP FUNCTION IF EXISTS urmail_coupon_redemption_guard();
DROP FUNCTION IF EXISTS urmail_coupon_redemption_capacity();
