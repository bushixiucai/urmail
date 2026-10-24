-- Migration 0026 down: payment core, billing cycle columns, and refunds table
DROP TABLE IF EXISTS refunds CASCADE;
DROP INDEX IF EXISTS invoices_tenant_order_idx;
ALTER TABLE invoices DROP COLUMN IF EXISTS order_id;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_id_tenant_unique;
ALTER TABLE subscriptions DROP CONSTRAINT IF EXISTS subscriptions_billing_cycle_check;
ALTER TABLE subscriptions DROP COLUMN IF EXISTS billing_cycle;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_billing_cycle_check;
ALTER TABLE orders DROP COLUMN IF EXISTS billing_cycle;
