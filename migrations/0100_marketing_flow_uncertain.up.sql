-- Preserve an uncertain marketing delivery.
-- A later call cannot advance or send it again.

ALTER TABLE marketing_flow_positions ADD COLUMN delivery_uncertain BOOLEAN NOT NULL DEFAULT false;

CREATE OR REPLACE FUNCTION urmail_marketing_flow_sendable(p_tenant bigint, p_flow bigint, p_email text) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE current_step text; current_unsubscribed boolean; current_wait timestamptz; current_uncertain boolean;
BEGIN
 SELECT step_key, unsubscribed, wait_until, delivery_uncertain INTO current_step, current_unsubscribed, current_wait, current_uncertain
 FROM marketing_flow_positions WHERE tenant_id=p_tenant AND flow_id=p_flow AND email=p_email;
 IF NOT FOUND THEN RETURN 'missing'; END IF;
 IF current_uncertain THEN RETURN 'uncertain'; END IF;
 IF current_unsubscribed OR current_wait>now() OR NOT urmail_marketing_flow_may_advance(p_tenant, p_flow, p_email) THEN RETURN 'blocked'; END IF;
 RETURN current_step;
END $$;

CREATE FUNCTION urmail_mark_marketing_flow_uncertain(p_tenant bigint, p_flow bigint, p_email text) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 UPDATE marketing_flow_positions SET delivery_uncertain=true, updated_at=now()
 WHERE tenant_id=p_tenant AND flow_id=p_flow AND email=p_email;
 IF NOT FOUND THEN RETURN 'missing'; END IF;
 RETURN 'uncertain';
END $$;
ALTER FUNCTION urmail_mark_marketing_flow_uncertain(bigint,bigint,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_mark_marketing_flow_uncertain(bigint,bigint,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_mark_marketing_flow_uncertain(bigint,bigint,text) TO urmail_platform;
