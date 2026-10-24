-- Stable merchant/environment identities survive credential rotation.
CREATE TABLE payment_merchants(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 provider text NOT NULL CHECK(provider IN('stripe','wechat','alipay','paypal','aggregated','tax_invoice')),
 environment text NOT NULL CHECK(environment IN('live','sandbox')),
 account_id text NOT NULL CHECK(account_id ~ '^[A-Za-z0-9_-]{1,120}$'),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 enabled boolean NOT NULL DEFAULT false,
 currencies text[] NOT NULL DEFAULT '{}',
 entrypoint text NOT NULL DEFAULT '',
 encrypted_config text NOT NULL CHECK(encrypted_config LIKE 'enc:v1:%'),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(provider,environment,account_id)
);
ALTER TABLE payment_merchants OWNER TO urmail_platform;
REVOKE ALL ON payment_merchants FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE payment_merchants ENABLE ROW LEVEL SECURITY;
CREATE POLICY no_tenant_access ON payment_merchants USING(false);
ALTER TABLE payments ADD COLUMN account_scope text NOT NULL DEFAULT 'legacy';
ALTER TABLE payment_events ADD COLUMN account_scope text NOT NULL DEFAULT 'legacy';
DROP INDEX payments_provider_txn_uniq;
CREATE UNIQUE INDEX payments_provider_txn_uniq ON payments(provider,account_scope,provider_txn_id);
ALTER TABLE payment_events DROP CONSTRAINT payment_events_provider_provider_event_id_key;
ALTER TABLE payment_events ADD CONSTRAINT payment_events_provider_scope_event_unique UNIQUE(provider,account_scope,provider_event_id);
CREATE TABLE payment_webhook_inbox(
 merchant_id bigint NOT NULL REFERENCES payment_merchants(id),
 event_id text NOT NULL CHECK(length(event_id) BETWEEN 1 AND 255),
 payload_hash text NOT NULL CHECK(payload_hash ~ '^[a-f0-9]{64}$'),
 payload jsonb NOT NULL,
 state text NOT NULL DEFAULT 'received' CHECK(state IN('received','processing','completed','uncertain','rejected')),
 processing_until timestamptz,
 result_code text,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(merchant_id,event_id)
);
ALTER TABLE payment_webhook_inbox OWNER TO urmail_platform;
REVOKE ALL ON payment_webhook_inbox FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE payment_webhook_inbox ENABLE ROW LEVEL SECURITY;
CREATE POLICY no_tenant_access ON payment_webhook_inbox USING(false);
REVOKE INSERT,UPDATE,DELETE ON payments FROM urmail_app;
REVOKE ALL ON payment_events FROM PUBLIC,urmail_app,urmail_dovecot;
CREATE FUNCTION urmail_payment_inbox_alert() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.state='uncertain' THEN
  INSERT INTO operational_alerts(alert_key,kind,state) VALUES('payment-webhook:'||NEW.merchant_id||':'||NEW.event_id,'payment_webhook_review','firing')
  ON CONFLICT(alert_key) DO UPDATE SET state='firing',last_seen_at=now(),resolved_at=NULL;
 ELSIF NEW.state='completed' THEN
  UPDATE operational_alerts SET state='resolved',last_seen_at=now(),resolved_at=now() WHERE alert_key='payment-webhook:'||NEW.merchant_id||':'||NEW.event_id;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_payment_inbox_alert() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_payment_inbox_alert() FROM PUBLIC;
CREATE TRIGGER payment_inbox_alert AFTER INSERT OR UPDATE OF state ON payment_webhook_inbox FOR EACH ROW EXECUTE FUNCTION urmail_payment_inbox_alert();
