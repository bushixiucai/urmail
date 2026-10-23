-- Duplicate commission history requires operator review, never deletion.
DO $$ BEGIN IF EXISTS(SELECT 1 FROM commissions GROUP BY channel_id,order_id HAVING count(*)>1) THEN RAISE EXCEPTION 'COMMISSION_DUPLICATES: review channel/order identifiers before migrating'; END IF; END $$;
ALTER TABLE commissions ADD CONSTRAINT commissions_channel_order_unique UNIQUE(channel_id,order_id), ADD CONSTRAINT commissions_amount_check CHECK(amount_cents>=0 AND bps BETWEEN 0 AND 10000);
ALTER TABLE orders ADD CONSTRAINT orders_amount_check CHECK(amount_cents>=0);
ALTER TABLE order_items ADD CONSTRAINT order_items_amount_check CHECK(quantity>0 AND unit_price_cents>=0 AND amount_cents>=0);
ALTER TABLE payments ADD CONSTRAINT payments_amount_check CHECK(amount_cents>=0);
ALTER TABLE invoices ADD CONSTRAINT invoices_amount_check CHECK(amount_cents>=0 AND (period_start IS NULL OR period_end IS NULL OR period_end>=period_start));
ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_period_check CHECK(period_end>period_start);
ALTER TABLE coupons ADD CONSTRAINT coupons_value_check CHECK((kind='percent' AND value BETWEEN 1 AND 100) OR (kind IN('fixed','trial_days') AND value>0)), ADD CONSTRAINT coupons_count_check CHECK(redeemed_count>=0 AND (max_redemptions IS NULL OR (max_redemptions>=0 AND redeemed_count<=max_redemptions)));
CREATE FUNCTION urmail_recall_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='INSERT' AND NEW.status<>'pending' THEN RAISE EXCEPTION 'recall must start pending' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' AND (OLD.status<>'pending' OR NEW.status NOT IN('pending','failed','expired')) THEN RAISE EXCEPTION 'remote delivery recall is not confirmed' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_recall_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_recall_guard() FROM PUBLIC;
CREATE TRIGGER recall_state_guard BEFORE INSERT OR UPDATE ON recall_requests FOR EACH ROW EXECUTE FUNCTION urmail_recall_guard();
