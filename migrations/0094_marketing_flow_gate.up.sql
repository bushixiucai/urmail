-- Marketing flow progression gate.
-- A position advances only while the recipient is subscribed and the tenant
-- may still send. This function never sends mail.

CREATE FUNCTION urmail_marketing_flow_may_advance(p_tenant bigint, p_flow bigint, p_email text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT urmail_mail_allowed(p_tenant)
  AND EXISTS(SELECT 1 FROM marketing_flow_positions p WHERE p.tenant_id=p_tenant AND p.flow_id=p_flow AND p.email=p_email AND NOT p.unsubscribed)
  AND NOT EXISTS(SELECT 1 FROM marketing_suppressions s WHERE s.tenant_id=p_tenant AND s.email=p_email);
$$;
ALTER FUNCTION urmail_marketing_flow_may_advance(bigint,bigint,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_marketing_flow_may_advance(bigint,bigint,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_marketing_flow_may_advance(bigint,bigint,text) TO urmail_platform;
