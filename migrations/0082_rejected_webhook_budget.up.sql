-- Unverified callback bodies never belong to the financial event inbox.
-- One global budget row serializes bounded diagnostic writes across processes.
CREATE TABLE payment_webhook_rejection_budget (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 minute_start timestamptz NOT NULL,
 writes integer NOT NULL CHECK (writes BETWEEN 0 AND 120)
);
INSERT INTO payment_webhook_rejection_budget VALUES(true,date_trunc('minute',now()),0);
ALTER TABLE payment_webhook_rejection_budget OWNER TO urmail_platform;
REVOKE ALL ON payment_webhook_rejection_budget FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE payment_webhook_rejection_budget ENABLE ROW LEVEL SECURITY;
CREATE POLICY no_tenant_access ON payment_webhook_rejection_budget USING(false);

CREATE TABLE payment_webhook_rejections (
 merchant_id bigint NOT NULL REFERENCES payment_merchants(id),
 bucket_start timestamptz NOT NULL,
 reason text NOT NULL CHECK(reason IN('verification_rejected','merchant_rejected')),
 occurrences bigint NOT NULL CHECK(occurrences BETWEEN 1 AND 9223372036854775807),
 last_payload_hash text NOT NULL CHECK(last_payload_hash ~ '^[a-f0-9]{64}$'),
 last_payload_bytes integer NOT NULL CHECK(last_payload_bytes BETWEEN 0 AND 1048576),
 last_seen_at timestamptz NOT NULL,
 PRIMARY KEY(merchant_id,bucket_start,reason)
);
CREATE INDEX payment_webhook_rejections_expiry ON payment_webhook_rejections(bucket_start);
ALTER TABLE payment_webhook_rejections OWNER TO urmail_platform;
REVOKE ALL ON payment_webhook_rejections FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE payment_webhook_rejections ENABLE ROW LEVEL SECURITY;
CREATE POLICY no_tenant_access ON payment_webhook_rejections USING(false);

CREATE FUNCTION urmail_record_rejected_webhook(p_merchant bigint,p_reason text,p_hash text,p_bytes integer)
RETURNS boolean LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE
 budget payment_webhook_rejection_budget%ROWTYPE;
 minute_now timestamptz := date_trunc('minute',statement_timestamp());
 hour_now timestamptz := date_trunc('hour',statement_timestamp());
BEGIN
 IF p_reason NOT IN('verification_rejected','merchant_rejected')
    OR p_hash IS NULL OR p_hash !~ '^[a-f0-9]{64}$'
    OR p_bytes IS NULL OR p_bytes<0 OR p_bytes>1048576 THEN
  RAISE EXCEPTION 'INVALID_REJECTED_WEBHOOK_METADATA' USING ERRCODE='23514';
 END IF;
 SELECT * INTO STRICT budget FROM payment_webhook_rejection_budget WHERE singleton FOR UPDATE;
 IF budget.minute_start=minute_now AND budget.writes>=120 THEN RETURN false; END IF;
 IF NOT EXISTS(SELECT 1 FROM payment_webhook_rejections WHERE merchant_id=p_merchant AND bucket_start=hour_now AND reason=p_reason)
    AND (SELECT count(*) FROM payment_webhook_rejections)>=10000 THEN RETURN false; END IF;
 UPDATE payment_webhook_rejection_budget SET minute_start=minute_now,
  writes=CASE WHEN budget.minute_start=minute_now THEN budget.writes+1 ELSE 1 END WHERE singleton;
 INSERT INTO payment_webhook_rejections VALUES(p_merchant,hour_now,p_reason,1,p_hash,p_bytes,statement_timestamp())
 ON CONFLICT(merchant_id,bucket_start,reason) DO UPDATE SET
  occurrences=LEAST(payment_webhook_rejections.occurrences,9223372036854775806)+1,
  last_payload_hash=EXCLUDED.last_payload_hash,last_payload_bytes=EXCLUDED.last_payload_bytes,last_seen_at=EXCLUDED.last_seen_at;
 RETURN true;
END $$;
ALTER FUNCTION urmail_record_rejected_webhook(bigint,text,text,integer) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_record_rejected_webhook(bigint,text,text,integer) FROM PUBLIC,urmail_app,urmail_dovecot;
