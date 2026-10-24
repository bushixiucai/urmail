-- Verified receipts remain durable even when fulfillment requires intervention.
CREATE TABLE payment_manual_reviews (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 order_id bigint NOT NULL REFERENCES orders(id),
 payment_id bigint NOT NULL REFERENCES payments(id),
 reason text NOT NULL,
 status text NOT NULL DEFAULT 'open' CHECK(status IN('open','fulfilled','refunded')),
 resolution_reason text,
 resolved_by bigint REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 resolved_at timestamptz,
 UNIQUE(payment_id),
 FOREIGN KEY(tenant_id,order_id) REFERENCES orders(tenant_id,id)
);
ALTER TABLE payment_manual_reviews OWNER TO urmail_platform;
REVOKE ALL ON payment_manual_reviews FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE payment_manual_reviews ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON payment_manual_reviews USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
GRANT SELECT ON payment_manual_reviews TO urmail_app;

CREATE TABLE payment_checkout_intents (
 order_id bigint PRIMARY KEY REFERENCES orders(id),
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 provider text NOT NULL,
 account_scope text NOT NULL,
 session_id text,
 checkout_url text,
 qr_code text,
 state text NOT NULL DEFAULT 'creating' CHECK(state IN('creating','ready','uncertain','cancel_requested','closed','paid','refunded')),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,order_id) REFERENCES orders(tenant_id,id)
);
ALTER TABLE payment_checkout_intents OWNER TO urmail_platform;
REVOKE ALL ON payment_checkout_intents FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE payment_checkout_intents ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON payment_checkout_intents USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
GRANT SELECT ON payment_checkout_intents TO urmail_app;
