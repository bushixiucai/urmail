-- BILL renewal attempts.
-- No provider here can charge a saved instrument. The worker therefore
-- records an explicit retryable failure and notifies the tenant. It never
-- marks an order paid or extends a subscription by itself.

CREATE TABLE subscription_renewal_attempts (
 id BIGSERIAL PRIMARY KEY,
 tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 subscription_id BIGINT NOT NULL,
 period_end TIMESTAMPTZ NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('pending','failed','succeeded','canceled')),
 attempts INT NOT NULL DEFAULT 0 CHECK (attempts>=0 AND attempts<=8),
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (subscription_id, period_end)
);
CREATE INDEX subscription_renewal_attempts_due ON subscription_renewal_attempts(next_attempt_at) WHERE state='pending';
ALTER TABLE subscription_renewal_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscription_renewal_attempts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON subscription_renewal_attempts USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint) WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint);
GRANT SELECT ON subscription_renewal_attempts TO urmail_app;
GRANT SELECT, INSERT, UPDATE ON subscription_renewal_attempts TO urmail_platform;
GRANT USAGE, SELECT ON SEQUENCE subscription_renewal_attempts_id_seq TO urmail_platform;
-- The platform runtime role owns its maintenance tables so restore-quarantine
-- tooling and fixture truncation keep working (same contract as 0028).
ALTER TABLE subscription_renewal_attempts OWNER TO urmail_platform;
