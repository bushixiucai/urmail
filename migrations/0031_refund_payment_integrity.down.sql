-- Migration 0031 down
REVOKE EXECUTE ON FUNCTION urmail_create_refund(bigint,bigint,bigint,text,bigint,text,text) FROM urmail_app;
DROP FUNCTION urmail_create_refund(bigint,bigint,bigint,text,bigint,text,text);
DROP INDEX IF EXISTS refunds_payment_outstanding_idx;
DROP INDEX IF EXISTS refunds_tenant_refund_no_unique;
ALTER TABLE refunds
    DROP CONSTRAINT IF EXISTS refunds_refund_no_nonempty,
    DROP CONSTRAINT IF EXISTS refunds_currency_nonempty,
    DROP CONSTRAINT IF EXISTS refunds_payment_required;
