DROP TRIGGER coupon_release_authority ON coupon_redemptions;
DROP FUNCTION urmail_coupon_release_authority();
GRANT UPDATE ON coupon_redemptions TO urmail_app;
GRANT DELETE ON orders TO urmail_app;
DROP TRIGGER order_intention_guard ON orders;
DROP FUNCTION urmail_order_intention_guard();
DROP TABLE billing_order_authorizations;
GRANT INSERT,UPDATE,DELETE ON invoices,commissions,order_items TO urmail_app;
GRANT UPDATE,DELETE ON channel_attribution TO urmail_app;
