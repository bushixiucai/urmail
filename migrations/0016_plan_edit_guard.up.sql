CREATE FUNCTION urmail_plan_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tid bigint;
BEGIN
 IF EXISTS(SELECT 1 FROM subscriptions WHERE plan_id=NEW.code AND status='past_due') AND (NEW.quota_bytes<>OLD.quota_bytes OR NEW.mailbox_limit<>OLD.mailbox_limit OR NEW.domain_limit<>OLD.domain_limit OR NEW.daily_send_limit<>OLD.daily_send_limit) THEN RAISE EXCEPTION 'SUBSCRIPTION_PAST_DUE' USING ERRCODE='23514'; END IF;
 FOR tid IN SELECT tenant_id FROM subscriptions WHERE plan_id=NEW.code AND status IN('active','trialing','past_due') ORDER BY tenant_id LOOP
  PERFORM pg_advisory_xact_lock(tid);
  UPDATE subscriptions SET plan_id=plan_id WHERE tenant_id=tid AND plan_id=NEW.code AND status IN('active','trialing','past_due');
 END LOOP;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_plan_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_plan_guard() FROM PUBLIC;
CREATE TRIGGER plan_resource_guard AFTER UPDATE ON plans FOR EACH ROW EXECUTE FUNCTION urmail_plan_guard();
