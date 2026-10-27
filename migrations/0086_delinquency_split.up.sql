-- BILL-005 correction: sending stays open for 7 days after period_end.
-- After that window, user sending and marketing stop. Login, historical
-- reading, and inbound delivery stay open. This does not delete mail or
-- clear a security suspension.

DO $$ DECLARE definition text; patched text; BEGIN
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 patched:=replace(definition,'s.period_end>now()','(s.period_end + interval ''7 days'' > now())');
 IF patched=definition THEN RAISE EXCEPTION 'SEND_GRACE_MAIL_GATE_UPDATE_REQUIRED'; END IF;
 EXECUTE patched;
END $$;

CREATE OR REPLACE FUNCTION urmail_mail_readable(p_tenant bigint) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT urmail_mail_allowed(p_tenant)
  OR EXISTS (
   SELECT 1 FROM tenants t JOIN subscriptions s ON s.tenant_id=t.id
   WHERE t.id=p_tenant AND t.status='active'
    AND s.status IN ('active','trialing','past_due')
    AND s.period_start<=now()
    AND (s.status<>'trialing' OR s.trial_ends_at IS NULL OR s.trial_ends_at>now())
  );
$$;
ALTER FUNCTION urmail_mail_readable(bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_mail_readable(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_mail_readable(bigint) TO urmail_app, urmail_dovecot;
