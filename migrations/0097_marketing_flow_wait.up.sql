-- Hold a marketing recipient until the recorded wait time.
-- Checking or committing before that time does not advance the step.

ALTER TABLE marketing_flow_positions ADD COLUMN wait_until TIMESTAMPTZ;

CREATE OR REPLACE FUNCTION urmail_marketing_flow_sendable(p_tenant bigint, p_flow bigint, p_email text) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE current_step text; current_unsubscribed boolean; current_wait timestamptz;
BEGIN
 SELECT step_key, unsubscribed, wait_until INTO current_step, current_unsubscribed, current_wait
 FROM marketing_flow_positions WHERE tenant_id=p_tenant AND flow_id=p_flow AND email=p_email;
 IF NOT FOUND THEN RETURN 'missing'; END IF;
 IF current_unsubscribed OR current_wait>now() OR NOT urmail_marketing_flow_may_advance(p_tenant, p_flow, p_email) THEN RETURN 'blocked'; END IF;
 RETURN current_step;
END $$;

CREATE OR REPLACE FUNCTION urmail_commit_marketing_flow_step(p_tenant bigint, p_flow bigint, p_email text, p_event text, p_next text) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE current_step text; current_unsubscribed boolean; current_wait timestamptz;
BEGIN
 SELECT step_key, unsubscribed, wait_until INTO current_step, current_unsubscribed, current_wait
 FROM marketing_flow_positions WHERE tenant_id=p_tenant AND flow_id=p_flow AND email=p_email FOR UPDATE;
 IF NOT FOUND THEN RETURN 'missing'; END IF;
 IF current_unsubscribed OR current_wait>now() OR NOT urmail_marketing_flow_may_advance(p_tenant, p_flow, p_email) THEN RETURN 'blocked'; END IF;
 IF p_event='unsubscribed' THEN current_unsubscribed:=true; END IF;
 IF p_next<>'' THEN current_step:=p_next; END IF;
 UPDATE marketing_flow_positions SET step_key=current_step, unsubscribed=current_unsubscribed, wait_until=NULL, updated_at=now()
 WHERE tenant_id=p_tenant AND flow_id=p_flow AND email=p_email;
 RETURN current_step;
END $$;
