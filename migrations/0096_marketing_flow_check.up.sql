-- Check a marketing-flow step without moving it.
-- The caller sends first and only then commits the next step.

CREATE FUNCTION urmail_marketing_flow_sendable(p_tenant bigint, p_flow bigint, p_email text) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE current_step text; current_unsubscribed boolean;
BEGIN
 SELECT step_key, unsubscribed INTO current_step, current_unsubscribed
 FROM marketing_flow_positions WHERE tenant_id=p_tenant AND flow_id=p_flow AND email=p_email;
 IF NOT FOUND THEN RETURN 'missing'; END IF;
 IF current_unsubscribed OR NOT urmail_marketing_flow_may_advance(p_tenant, p_flow, p_email) THEN RETURN 'blocked'; END IF;
 RETURN current_step;
END $$;
ALTER FUNCTION urmail_marketing_flow_sendable(bigint,bigint,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_marketing_flow_sendable(bigint,bigint,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_marketing_flow_sendable(bigint,bigint,text) TO urmail_platform;
