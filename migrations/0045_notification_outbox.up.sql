-- One durable source for in-app and verified-contact notification delivery.
CREATE TABLE notification_outbox (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint REFERENCES tenants(id),
 user_id bigint REFERENCES users(id),
 kind text NOT NULL,
 message_key text NOT NULL,
 params jsonb NOT NULL DEFAULT '{}'::jsonb,
 dedupe_key text NOT NULL UNIQUE,
 channel text NOT NULL CHECK(channel IN('in_app','email')),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN('pending','delivered','uncertain','failed','blocked')),
 recipient text,
 recipient_verified boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 read_at timestamptz,
 delivered_at timestamptz,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz,
 fence bigint NOT NULL DEFAULT 0,
 attempts integer NOT NULL DEFAULT 0,
 CHECK(channel<>'email' OR (recipient IS NOT NULL AND recipient_verified))
);
ALTER TABLE notification_outbox OWNER TO urmail_platform;
REVOKE ALL ON notification_outbox FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE notification_outbox ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_outbox
 USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND
 (user_id IS NULL OR user_id=nullif(current_setting('urmail.user_id',true),'')::bigint));
GRANT SELECT ON notification_outbox TO urmail_app;

CREATE TABLE operational_alerts (
 alert_key text PRIMARY KEY,
 tenant_id bigint REFERENCES tenants(id),
 kind text NOT NULL,
 state text NOT NULL CHECK(state IN('firing','resolved')),
 first_seen_at timestamptz NOT NULL DEFAULT now(),
 last_seen_at timestamptz NOT NULL DEFAULT now(),
 resolved_at timestamptz
);
ALTER TABLE operational_alerts OWNER TO urmail_platform;
REVOKE ALL ON operational_alerts FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE operational_alerts ENABLE ROW LEVEL SECURITY;
CREATE POLICY no_tenant_access ON operational_alerts USING(false);
