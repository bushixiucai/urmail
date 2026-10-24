-- Migration 0029: tenant-safe atomic coupon consumption for checkout.
--
-- Privilege model (M5 review v2, P0-1): the tenant path must NOT hand the app
-- role a SECURITY DEFINER function. urmail_redeem_coupon now runs with the
-- caller's own privileges, so every read and write it performs is subject to
-- the tenant RLS policies and the tenant_id it writes is the one the session
-- published - the app role gains no bypass-RLS capability. Global state that
-- a tenant must never touch (the coupons.redeemed_count capacity counter) is
-- maintained exclusively by trigger functions that no role can call directly.
--
-- Coupon eligibility (active / validity window) is checked before the insert
-- so the caller gets a clean status; capacity is enforced by the trigger so
-- it stays race-free against other tenants.
CREATE FUNCTION urmail_redeem_coupon(p_code text, p_order_id bigint)
RETURNS text LANGUAGE plpgsql
SET search_path=pg_catalog,public AS $$
DECLARE
    tid bigint;
    order_tenant bigint;
    coupon coupons%ROWTYPE;
BEGIN
    tid := nullif(current_setting('urmail.tenant_id', true), '')::bigint;
    IF tid IS NULL OR p_order_id IS NULL THEN
        RAISE EXCEPTION 'COUPON_TENANT_REQUIRED' USING ERRCODE='42501';
    END IF;
    -- RLS filters orders to the session tenant: an order id belonging to
    -- another tenant is simply not visible here.
    SELECT tenant_id INTO order_tenant FROM orders
      WHERE id = p_order_id AND status = 'pending';
    IF order_tenant IS NULL THEN
        RETURN 'invalid';
    END IF;
    IF order_tenant <> tid THEN
        RAISE EXCEPTION 'COUPON_ORDER_TENANT_MISMATCH' USING ERRCODE='42501';
    END IF;
    SELECT * INTO coupon FROM coupons
      WHERE code = p_code AND active
        AND (valid_from IS NULL OR valid_from <= now())
        AND (valid_to IS NULL OR valid_to > now());
    IF NOT FOUND THEN
        RETURN 'invalid';
    END IF;
    BEGIN
        INSERT INTO coupon_redemptions(coupon_code, tenant_id, order_id)
          VALUES (p_code, tid, p_order_id)
          ON CONFLICT (coupon_code, tenant_id) DO NOTHING;
        IF NOT FOUND THEN
            RETURN 'already_redeemed';
        END IF;
    EXCEPTION
        -- The capacity trigger refused the reservation. The subtransaction
        -- rolls back the partially applied insert and counter update.
        WHEN SQLSTATE 'CU001' THEN RETURN 'invalid';
        WHEN SQLSTATE 'CU002' THEN RETURN 'exhausted';
    END;
    RETURN 'redeemed';
END
$$;
ALTER FUNCTION urmail_redeem_coupon(text,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_redeem_coupon(text,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_redeem_coupon(text,bigint) TO urmail_app;

-- Coupon state guard. This is the ONLY writer of coupons.redeemed_count:
-- neither the tenant path nor the platform path touches the counter directly,
-- so it can never drift from the redemption rows. Deleting the reservation of
-- a still-pending order releases the capacity again (order failed or
-- abandoned), while the reservation of a settled order is earned and
-- immutable, so a tenant cannot free a used discount and redeem again.
CREATE FUNCTION urmail_coupon_redemption_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF EXISTS (SELECT 1 FROM orders o WHERE o.id = OLD.order_id AND o.status = 'paid') THEN
            RAISE EXCEPTION 'COUPON_REDEMPTION_SETTLED' USING ERRCODE='CU003';
        END IF;
        UPDATE coupons SET redeemed_count = GREATEST(redeemed_count - 1, 0)
          WHERE code = OLD.coupon_code;
        RETURN OLD;
    END IF;
    -- BEFORE INSERT: reject unknown or inactive coupons for anyone inserting
    -- behind the API's back, so capacity can never be consumed by a row that
    -- references a dead coupon.
    IF NOT EXISTS (
        SELECT 1 FROM coupons
         WHERE code = NEW.coupon_code AND active
           AND (valid_from IS NULL OR valid_from <= now())
           AND (valid_to IS NULL OR valid_to > now())
    ) THEN
        RAISE EXCEPTION 'COUPON_INVALID' USING ERRCODE='CU001';
    END IF;
    RETURN NEW;
END
$$;
ALTER FUNCTION urmail_coupon_redemption_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_coupon_redemption_guard() FROM PUBLIC;

-- Capacity check runs after the row insert: raising here aborts the whole
-- statement, so an over-capacity reservation is never persisted and the
-- counter never moves. Row-level FOR UPDATE on the coupon serialises
-- concurrent redemptions of the same code.
CREATE FUNCTION urmail_coupon_redemption_capacity() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public AS $$
DECLARE
    maxr int;
    cnt int;
BEGIN
    SELECT max_redemptions INTO maxr FROM coupons WHERE code = NEW.coupon_code FOR UPDATE;
    IF maxr IS NOT NULL THEN
        SELECT count(*) INTO cnt FROM coupon_redemptions WHERE coupon_code = NEW.coupon_code;
        IF cnt > maxr THEN
            RAISE EXCEPTION 'COUPON_EXHAUSTED' USING ERRCODE='CU002';
        END IF;
    END IF;
    UPDATE coupons
       SET redeemed_count = (SELECT count(*) FROM coupon_redemptions WHERE coupon_code = NEW.coupon_code)
     WHERE code = NEW.coupon_code;
    RETURN NULL;
END
$$;
ALTER FUNCTION urmail_coupon_redemption_capacity() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_coupon_redemption_capacity() FROM PUBLIC;

CREATE TRIGGER coupon_redemptions_guard
    BEFORE INSERT OR DELETE ON coupon_redemptions
    FOR EACH ROW EXECUTE FUNCTION urmail_coupon_redemption_guard();

CREATE TRIGGER coupon_redemptions_capacity
    AFTER INSERT ON coupon_redemptions
    FOR EACH ROW EXECUTE FUNCTION urmail_coupon_redemption_capacity();

-- Explicit platform-initiated redemptions remain supported by the admin API.
-- Its platform role validates the requested tenant against the order and uses
-- the same capacity lock and uniqueness invariant as tenant checkout.
CREATE FUNCTION urmail_redeem_coupon_for_tenant(p_code text,p_tenant_id bigint,p_order_id bigint)
RETURNS text LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public AS $$
DECLARE
    coupon coupons%ROWTYPE;
    order_tenant bigint;
BEGIN
    IF p_tenant_id IS NULL OR p_tenant_id <= 0 THEN
        RAISE EXCEPTION 'COUPON_TENANT_REQUIRED' USING ERRCODE='22023';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM tenants WHERE id=p_tenant_id) THEN
        RETURN 'invalid';
    END IF;
    IF p_order_id IS NULL THEN
        RETURN 'invalid';
    END IF;
    SELECT tenant_id INTO order_tenant FROM orders WHERE id=p_order_id FOR UPDATE;
    IF order_tenant IS DISTINCT FROM p_tenant_id THEN
        RAISE EXCEPTION 'COUPON_ORDER_TENANT_MISMATCH' USING ERRCODE='23514';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM orders WHERE id=p_order_id AND status='pending') THEN
        RETURN 'invalid';
    END IF;
    IF EXISTS (SELECT 1 FROM coupon_redemptions WHERE coupon_code=p_code AND tenant_id=p_tenant_id AND order_id=p_order_id) THEN
        RETURN 'already_redeemed';
    END IF;
    IF EXISTS (SELECT 1 FROM coupon_redemptions WHERE coupon_code=p_code AND tenant_id=p_tenant_id) THEN
        -- One redemption per coupon per tenant, and it is bound to the order
        -- that consumed it: reusing the coupon for a second order is invalid,
        -- not a duplicate replay of the same request.
        RETURN 'invalid';
    END IF;
    SELECT * INTO coupon FROM coupons
      WHERE code=p_code AND active
        AND (valid_from IS NULL OR valid_from <= now())
        AND (valid_to IS NULL OR valid_to > now())
      FOR UPDATE;
    IF NOT FOUND THEN RETURN 'invalid'; END IF;
    BEGIN
        INSERT INTO coupon_redemptions(coupon_code,tenant_id,order_id)
          VALUES(p_code,p_tenant_id,p_order_id) ON CONFLICT (coupon_code,tenant_id) DO NOTHING;
        IF NOT FOUND THEN
            RETURN 'already_redeemed';
        END IF;
    EXCEPTION
        WHEN SQLSTATE 'CU001' THEN RETURN 'invalid';
        WHEN SQLSTATE 'CU002' THEN RETURN 'exhausted';
    END;
    RETURN 'redeemed';
END
$$;
ALTER FUNCTION urmail_redeem_coupon_for_tenant(text,bigint,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_redeem_coupon_for_tenant(text,bigint,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_redeem_coupon_for_tenant(text,bigint,bigint) TO urmail_platform;
