ALTER TABLE payment_merchants ADD COLUMN checkout_limits jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(checkout_limits)='object');
COMMENT ON COLUMN payment_merchants.checkout_limits IS 'Explicit per-currency merchant/API checkout bounds; empty means unavailable for new payments, historical fact reconciliation remains enabled';
