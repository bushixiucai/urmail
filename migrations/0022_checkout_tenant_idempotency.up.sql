-- Idempotency keys identify one tenant's operation, never another tenant's.
ALTER TABLE orders DROP CONSTRAINT orders_idempotency_key_key;
CREATE UNIQUE INDEX orders_tenant_idempotency_key
 ON orders(tenant_id,idempotency_key) WHERE idempotency_key IS NOT NULL;
