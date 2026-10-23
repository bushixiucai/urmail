DROP TRIGGER recall_state_guard ON recall_requests;
DROP FUNCTION urmail_recall_guard();
ALTER TABLE coupons DROP CONSTRAINT coupons_value_check,DROP CONSTRAINT coupons_count_check;
ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_period_check;
ALTER TABLE invoices DROP CONSTRAINT invoices_amount_check;
ALTER TABLE payments DROP CONSTRAINT payments_amount_check;
ALTER TABLE order_items DROP CONSTRAINT order_items_amount_check;
ALTER TABLE orders DROP CONSTRAINT orders_amount_check;
ALTER TABLE commissions DROP CONSTRAINT commissions_channel_order_unique,DROP CONSTRAINT commissions_amount_check;
