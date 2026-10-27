DO $$ DECLARE definition text; patched text; BEGIN
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 patched:=replace(definition,'(s.period_end + interval ''7 days'' > now())','s.period_end>now()');
 IF patched=definition THEN RAISE EXCEPTION 'SEND_GRACE_MAIL_GATE_RESTORE_REQUIRED'; END IF;
 EXECUTE patched;
END $$;
CREATE OR REPLACE FUNCTION urmail_mail_readable(p_tenant bigint) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT urmail_mail_allowed(p_tenant)
  OR EXISTS (
   SELECT 1 FROM subscriptions s
   WHERE s.tenant_id=p_tenant AND s.status IN ('active','trialing','past_due')
    AND s.period_end IS NOT NULL AND s.period_end<=now() AND s.period_end + interval '7 days' > now()
  );
$$;
