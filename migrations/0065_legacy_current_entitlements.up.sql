-- The first protected future term records the exact boundary at which an
-- already active legacy entitlement hands over to the new ledger. Do not
-- mint a historical order or infer a longer grace interval from later renewals.
CREATE FUNCTION urmail_legacy_current_entitlement_end(p_tenant bigint) RETURNS timestamptz
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE boundary timestamptz;
BEGIN
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint
 AND NOT (current_setting('role',true) IN('urmail_platform','urmail_migrate','urmail_dovecot')
  OR (current_setting('role',true)='none' AND (pg_has_role(session_user,'urmail_platform','USAGE') OR pg_has_role(session_user,'urmail_dovecot','USAGE')))) THEN
  RETURN NULL;
 END IF;
 -- Revoked terms remain evidence of the original handover. Ignoring them
 -- would give free legacy service across a refunded future-period gap.
 SELECT min(t.period_start) INTO boundary FROM subscription_terms t
 WHERE t.tenant_id=p_tenant AND t.basis_order_id IS NULL;
 IF boundary>now() AND EXISTS(SELECT 1 FROM subscriptions s WHERE s.tenant_id=p_tenant
  AND s.plan_id<>'free' AND s.status IN('active','past_due')
  AND s.period_start<=now() AND s.period_end>=boundary) THEN
  RETURN boundary;
 END IF;
 RETURN NULL;
END $$;
ALTER FUNCTION urmail_legacy_current_entitlement_end(bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_legacy_current_entitlement_end(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_legacy_current_entitlement_end(bigint) TO urmail_app,urmail_dovecot;

DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 definition:=replace(definition,'/* refund_term_gate */ (','/* refund_term_gate */ (/* legacy_current_gate */ COALESCE(urmail_legacy_current_entitlement_end(s.tenant_id)>now(),false) OR ');
 IF strpos(definition,'legacy_current_gate')=0 THEN RAISE EXCEPTION 'LEGACY_MAIL_GATE_UPDATE_REQUIRED'; END IF;
 EXECUTE definition;
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'/* trial_after_refund */ EXISTS','/* trial_after_refund */ NOT COALESCE(urmail_legacy_current_entitlement_end(tid)>now(),false) AND /* legacy_current_gate */ EXISTS');
 IF strpos(definition,'legacy_current_gate')=0 THEN RAISE EXCEPTION 'LEGACY_RESOURCE_GATE_UPDATE_REQUIRED'; END IF;
 EXECUTE definition;
END $$;
