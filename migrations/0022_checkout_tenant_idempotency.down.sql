-- This intentionally refuses rollback if tenant-local keys now collide globally.
-- No order may be deleted or silently renamed to make an old schema fit.
ALTER TABLE orders ADD CONSTRAINT orders_idempotency_key_key UNIQUE(idempotency_key);
DROP INDEX orders_tenant_idempotency_key;
