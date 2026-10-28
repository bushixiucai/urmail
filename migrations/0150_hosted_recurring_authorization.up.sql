-- Hosted setup is not consent to charge. Provider creation intent is durable
-- before network I/O, and uncertain setup creation is never blindly repeated.
CREATE TABLE recurring_setup_intents (
 id bigserial PRIMARY KEY,
 tenant_id bigint NOT NULL,
 subscription_id bigint NOT NULL,
 actor_id bigint NOT NULL REFERENCES users(id),
 merchant_id bigint NOT NULL REFERENCES payment_merchants(id),
 request_key text NOT NULL CHECK(length(request_key) BETWEEN 8 AND 200),
 request_id text NOT NULL UNIQUE CHECK(length(request_id)=36),
 quote_digest text NOT NULL CHECK(length(quote_digest)=64),
 currency text NOT NULL CHECK(currency IN('CNY','USD')),
 expected_period_end timestamptz NOT NULL,
 merchant_config_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'creating' CHECK(state IN('creating','ready','uncertain','verified','canceled','expired','held')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 provider_reference text NOT NULL DEFAULT '' CHECK(length(provider_reference)<=200),
 hosted_url text NOT NULL DEFAULT '' CHECK(length(hosted_url)<=4096),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '30 minutes',
 binding_id bigint,
 last_error text NOT NULL DEFAULT '' CHECK(length(last_error)<=128),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,subscription_id) REFERENCES subscriptions(tenant_id,id),
 FOREIGN KEY(tenant_id,binding_id) REFERENCES subscription_renewal_authorizations(tenant_id,id),
 UNIQUE(tenant_id,request_key)
);
CREATE UNIQUE INDEX recurring_setup_provider_identity ON recurring_setup_intents(merchant_id,provider_reference) WHERE provider_reference<>'';
CREATE INDEX recurring_setup_tenant_created ON recurring_setup_intents(tenant_id,created_at DESC);
ALTER TABLE recurring_setup_intents OWNER TO urmail_platform;
ALTER TABLE recurring_setup_intents ENABLE ROW LEVEL SECURITY;
ALTER TABLE recurring_setup_intents FORCE ROW LEVEL SECURITY;
CREATE POLICY recurring_setup_platform ON recurring_setup_intents TO urmail_platform USING(true) WITH CHECK(true);
CREATE POLICY recurring_setup_tenant ON recurring_setup_intents TO urmail_app
 USING(tenant_id=NULLIF(current_setting('urmail.tenant_id',true),'')::bigint)
 WITH CHECK(tenant_id=NULLIF(current_setting('urmail.tenant_id',true),'')::bigint);
REVOKE ALL ON recurring_setup_intents FROM PUBLIC,urmail_app,urmail_dovecot;
