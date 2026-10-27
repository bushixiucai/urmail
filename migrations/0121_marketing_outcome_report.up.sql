-- MKT-005: campaign outcome reporting with immediate unsubscribe honoring.
-- Counters are maintained by triggers so the report is incremental and
-- consistent with the durable recipient facts, never recomputed by workers.

CREATE TABLE marketing_campaign_stats (
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 campaign_id bigint NOT NULL,
 sent bigint NOT NULL DEFAULT 0 CHECK(sent>=0),
 failed bigint NOT NULL DEFAULT 0 CHECK(failed>=0),
 uncertain bigint NOT NULL DEFAULT 0 CHECK(uncertain>=0),
 unsubscribed bigint NOT NULL DEFAULT 0 CHECK(unsubscribed>=0),
 bounced bigint NOT NULL DEFAULT 0 CHECK(bounced>=0),
 bounced_unknown bigint NOT NULL DEFAULT 0 CHECK(bounced_unknown>=0),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,campaign_id),
 FOREIGN KEY(tenant_id,campaign_id) REFERENCES marketing_campaigns(tenant_id,id) ON DELETE CASCADE
);
ALTER TABLE marketing_campaign_stats OWNER TO urmail_platform;
REVOKE ALL ON marketing_campaign_stats FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE marketing_campaign_stats ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON marketing_campaign_stats USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint) WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
GRANT SELECT ON marketing_campaign_stats TO urmail_app;

-- Only final, completed_at-stamped outcomes are counted; the retry loop
-- (failed -> queued) never inflates the failed counter.
CREATE FUNCTION urmail_bump_marketing_campaign_stats() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE p_tenant bigint; p_campaign bigint; p_column text;
BEGIN
 p_tenant:=NEW.tenant_id; p_campaign:=NEW.campaign_id;
 p_column:=CASE NEW.status
  WHEN 'sent' THEN 'sent'
  WHEN 'failed' THEN 'failed'
  WHEN 'uncertain' THEN 'uncertain'
  WHEN 'bounced' THEN 'bounced'
  WHEN 'suppressed' THEN 'unsubscribed'
  ELSE NULL END;
 IF p_column IS NULL THEN RETURN NEW; END IF;
 EXECUTE format('INSERT INTO marketing_campaign_stats(tenant_id,campaign_id,%I) VALUES($1,$2,1) ON CONFLICT(tenant_id,campaign_id) DO UPDATE SET %I=marketing_campaign_stats.%I+1,updated_at=now()',p_column,p_column,p_column) USING p_tenant,p_campaign;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_bump_marketing_campaign_stats() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_bump_marketing_campaign_stats() FROM PUBLIC;
CREATE TRIGGER marketing_campaign_stats_bump AFTER UPDATE OF status,completed_at ON marketing_recipients
FOR EACH ROW WHEN (NEW.completed_at IS NOT NULL AND (OLD.completed_at IS NULL OR OLD.status IS DISTINCT FROM NEW.status))
EXECUTE FUNCTION urmail_bump_marketing_campaign_stats();

-- A bounce reported by the gateway feedback stream AFTER an accepted send is
-- an unknown-outcome bounce: the acceptance already counted the campaign send.
CREATE FUNCTION urmail_bump_marketing_bounce_unknown() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE p_campaign bigint;
BEGIN
 IF NEW.status<>'bounce' THEN RETURN NEW; END IF;
 SELECT campaign_id INTO p_campaign FROM marketing_recipients WHERE tenant_id=NEW.tenant_id AND id=NEW.recipient_id AND status='sent';
 IF p_campaign IS NULL THEN RETURN NEW; END IF;
 UPDATE marketing_campaign_stats SET bounced_unknown=bounced_unknown+1,updated_at=now() WHERE tenant_id=NEW.tenant_id AND campaign_id=p_campaign;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_bump_marketing_bounce_unknown() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_bump_marketing_bounce_unknown() FROM PUBLIC;
CREATE TRIGGER marketing_feedback_bounce_unknown AFTER INSERT ON marketing_feedback
FOR EACH ROW EXECUTE FUNCTION urmail_bump_marketing_bounce_unknown();

-- An unsubscribe that lands after a claim but before DATA terminates the
-- attempt immediately: the reservation is refunded and the recipient is
-- recorded as suppressed with an explicit reason. It is never re-sent.
CREATE FUNCTION urmail_mark_marketing_recipient_revoked(p_tenant bigint,p_recipient bigint,p_owner text,p_fence bigint) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r marketing_recipients%ROWTYPE;
BEGIN
 IF current_setting('role',true)<>'urmail_platform' THEN RAISE EXCEPTION 'MARKETING_WORKER_REQUIRED' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT * INTO r FROM marketing_recipients WHERE tenant_id=p_tenant AND id=p_recipient FOR UPDATE;
 IF NOT FOUND OR r.status<>'submitting' OR r.lease_owner IS DISTINCT FROM p_owner OR r.fence<>p_fence THEN RETURN false; END IF;
 IF r.reserved THEN
  UPDATE marketing_quota_windows SET used_count=GREATEST(0,used_count-1) WHERE tenant_id=p_tenant AND id=r.window_id;
 END IF;
 UPDATE marketing_recipients SET status='suppressed',reserved=false,result_code='unsubscribed_mid_run',completed_at=now(),lease_owner=NULL,lease_until=NULL WHERE id=r.id;
 RETURN true;
END $$;
ALTER FUNCTION urmail_mark_marketing_recipient_revoked(bigint,bigint,text,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_mark_marketing_recipient_revoked(bigint,bigint,text,bigint) FROM PUBLIC;

-- A permanent precondition failure at execution time skips the recipient
-- forever with a recorded reason; the overdue policy forbids retrying after
-- the tenant lost entitlement or misconfigured the channel.
CREATE FUNCTION urmail_skip_marketing_recipient(p_tenant bigint,p_recipient bigint,p_owner text,p_fence bigint,p_reason text) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r marketing_recipients%ROWTYPE;
BEGIN
 IF current_setting('role',true)<>'urmail_platform' OR p_reason NOT IN('entitlement_denied','quota_unconfigured','channel_unavailable','sender_unavailable','timezone_invalid') THEN RAISE EXCEPTION 'MARKETING_WORKER_REQUIRED' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT * INTO r FROM marketing_recipients WHERE tenant_id=p_tenant AND id=p_recipient FOR UPDATE;
 IF NOT FOUND OR r.status<>'leased' OR r.lease_owner IS DISTINCT FROM p_owner OR r.fence<>p_fence THEN RETURN false; END IF;
 IF r.reserved THEN
  UPDATE marketing_quota_windows SET used_count=GREATEST(0,used_count-1) WHERE tenant_id=p_tenant AND id=r.window_id;
 END IF;
 UPDATE marketing_recipients SET status='canceled',reserved=false,result_code=p_reason,completed_at=now(),lease_owner=NULL,lease_until=NULL WHERE id=r.id;
 RETURN true;
END $$;
ALTER FUNCTION urmail_skip_marketing_recipient(bigint,bigint,text,bigint,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_skip_marketing_recipient(bigint,bigint,text,bigint,text) FROM PUBLIC;
