-- BILL-005: readable bit used by inbound delivery.
-- Migration 0086 gives urmail_mail_allowed the confirmed 7-day send grace
-- and then keeps urmail_mail_readable true after that window.

CREATE FUNCTION urmail_mail_readable(p_tenant bigint) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT urmail_mail_allowed(p_tenant);
$$;
ALTER FUNCTION urmail_mail_readable(bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_mail_readable(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_mail_readable(bigint) TO urmail_app, urmail_dovecot;
