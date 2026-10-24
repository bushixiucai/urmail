-- Migration 0031: enforce refund/payment integrity and serialize reservations.
ALTER TABLE refunds
    ADD CONSTRAINT refunds_payment_required CHECK (payment_id IS NOT NULL),
    ADD CONSTRAINT refunds_currency_nonempty CHECK (currency <> ''),
    ADD CONSTRAINT refunds_refund_no_nonempty CHECK (refund_no <> '');
CREATE UNIQUE INDEX refunds_tenant_refund_no_unique ON refunds (tenant_id, refund_no);
CREATE INDEX refunds_payment_outstanding_idx ON refunds (tenant_id, payment_id)
    WHERE status IN ('pending', 'approved', 'processing', 'completed');

-- Reservation routine for refund requests.
--
-- Privilege model (M5 review v2, P0-2): the previous revision was SECURITY
-- DEFINER and trusted the caller-supplied tenant id, so any session could
-- write a refund row for an arbitrary tenant while bypassing RLS. The routine
-- now runs with the caller's own privileges: payments and refunds are read
-- and written through the tenant RLS policies, the tenant id is taken from
-- the session context and the caller-supplied tenant id is only accepted when
-- it matches, and no role gains a bypass-RLS write primitive.
--
-- Every rejection raises a distinct condition so the API can tell the caller
-- why a refund was refused instead of returning a silent empty result.
CREATE FUNCTION urmail_create_refund(
    p_tenant_id bigint, p_order_id bigint, p_payment_id bigint,
    p_refund_no text, p_amount_cents bigint, p_currency text, p_reason text
) RETURNS SETOF refunds LANGUAGE plpgsql
SET search_path=pg_catalog,public AS $$
DECLARE
    session_tenant bigint;
    payment payments%ROWTYPE;
    outstanding bigint;
    created refunds%ROWTYPE;
BEGIN
    IF p_tenant_id IS NULL OR p_tenant_id <= 0 OR p_order_id IS NULL OR p_payment_id IS NULL
       OR p_refund_no IS NULL OR p_refund_no = '' OR p_amount_cents IS NULL OR p_amount_cents <= 0
       OR p_currency IS NULL OR p_currency = '' THEN
        RAISE EXCEPTION 'REFUND_INVALID_PARAMS' USING ERRCODE='22023';
    END IF;
    session_tenant := nullif(current_setting('urmail.tenant_id', true), '')::bigint;
    IF session_tenant IS NULL THEN
        RAISE EXCEPTION 'REFUND_TENANT_REQUIRED' USING ERRCODE='42501';
    END IF;
    IF p_tenant_id <> session_tenant THEN
        RAISE EXCEPTION 'REFUND_TENANT_MISMATCH' USING ERRCODE='42501';
    END IF;
    PERFORM pg_advisory_xact_lock(p_payment_id);
    -- RLS restricts payments to the session tenant; a foreign payment id is
    -- not visible here and the lookup simply finds nothing.
    SELECT * INTO payment FROM payments
    WHERE tenant_id = session_tenant AND id = p_payment_id AND order_id = p_order_id
    FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'REFUND_PAYMENT_NOT_FOUND' USING ERRCODE='23514';
    END IF;
    IF payment.status <> 'succeeded' THEN
        RAISE EXCEPTION 'REFUND_PAYMENT_NOT_SUCCESSFUL' USING ERRCODE='23514';
    END IF;
    IF payment.currency <> p_currency THEN
        RAISE EXCEPTION 'REFUND_CURRENCY_MISMATCH' USING ERRCODE='23514';
    END IF;
    IF EXISTS (SELECT 1 FROM refunds WHERE tenant_id = session_tenant AND refund_no = p_refund_no) THEN
        RAISE EXCEPTION 'REFUND_DUPLICATE' USING ERRCODE='23505';
    END IF;
    SELECT COALESCE(sum(amount_cents),0) INTO outstanding FROM refunds
    WHERE tenant_id = session_tenant AND payment_id = p_payment_id
      AND status IN ('pending','approved','processing','completed');
    IF outstanding + p_amount_cents > payment.amount_cents THEN
        RAISE EXCEPTION 'REFUND_EXCEEDS_PAYMENT' USING ERRCODE='23514';
    END IF;
    INSERT INTO refunds(tenant_id,order_id,payment_id,refund_no,amount_cents,currency,reason,status)
    VALUES(session_tenant,p_order_id,p_payment_id,p_refund_no,p_amount_cents,p_currency,COALESCE(p_reason,''),'pending')
    RETURNING * INTO created;
    RETURN NEXT created;
    RETURN;
END
$$;
ALTER FUNCTION urmail_create_refund(bigint,bigint,bigint,text,bigint,text,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_create_refund(bigint,bigint,bigint,text,bigint,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_create_refund(bigint,bigint,bigint,text,bigint,text,text) TO urmail_app;
