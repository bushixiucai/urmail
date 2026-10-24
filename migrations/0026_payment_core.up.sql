-- Migration 0026: payment core, billing cycle columns, and refunds table
ALTER TABLE orders ADD COLUMN billing_cycle TEXT NOT NULL DEFAULT 'monthly';
ALTER TABLE orders ADD CONSTRAINT orders_billing_cycle_check CHECK (billing_cycle IN ('monthly', 'quarterly', 'annual'));

ALTER TABLE subscriptions ADD COLUMN billing_cycle TEXT NOT NULL DEFAULT 'monthly';
ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_billing_cycle_check CHECK (billing_cycle IN ('monthly', 'quarterly', 'annual'));

ALTER TABLE payments ADD CONSTRAINT payments_id_tenant_unique UNIQUE (id, tenant_id);

-- A settlement must stay traceable to the order that produced it so an
-- invoice remains reproducible after a later plan price edit (M5-14).
ALTER TABLE invoices ADD COLUMN order_id BIGINT REFERENCES orders(id) ON DELETE SET NULL;
CREATE INDEX invoices_tenant_order_idx ON invoices (tenant_id, order_id);

CREATE TABLE refunds (
    id                 BIGSERIAL PRIMARY KEY,
    tenant_id          BIGINT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    order_id           BIGINT NOT NULL,
    payment_id         BIGINT,
    refund_no          TEXT NOT NULL UNIQUE,
    amount_cents       BIGINT NOT NULL CHECK (amount_cents > 0),
    currency           TEXT NOT NULL DEFAULT 'CNY',
    reason             TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'pending',
    provider_refund_no TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at       TIMESTAMPTZ,
    CONSTRAINT refunds_status_check
        CHECK (status IN ('pending', 'approved', 'processing', 'completed', 'rejected', 'failed')),
    CONSTRAINT refunds_order_tenant_fk
        FOREIGN KEY (order_id, tenant_id) REFERENCES orders(id, tenant_id) ON DELETE RESTRICT,
    CONSTRAINT refunds_payment_tenant_fk
        FOREIGN KEY (payment_id, tenant_id) REFERENCES payments(id, tenant_id) ON DELETE RESTRICT
);

CREATE INDEX ON refunds (tenant_id, created_at DESC);
CREATE INDEX ON refunds (order_id);
CREATE INDEX ON refunds (status);

ALTER TABLE refunds ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON refunds
    USING (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    WITH CHECK (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint);

GRANT SELECT, INSERT, UPDATE, DELETE ON refunds TO urmail_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON refunds TO urmail_platform;
-- Align with 0001: the platform role owns every table so it holds the full
-- privilege set (including TRUNCATE) and bypasses the tenant RLS policies.
ALTER TABLE refunds OWNER TO urmail_platform;

