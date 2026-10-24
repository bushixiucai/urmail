-- Migration 0027: onboarding tasks and delivery telemetry
CREATE TABLE onboarding_tasks (
    id             BIGSERIAL PRIMARY KEY,
    tenant_id      BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    domain_id      BIGINT REFERENCES domains(id) ON DELETE CASCADE,
    order_id       BIGINT REFERENCES orders(id) ON DELETE SET NULL,
    status         TEXT NOT NULL DEFAULT 'pending',
    step           TEXT NOT NULL DEFAULT 'domain_verification',
    attempts       INT NOT NULL DEFAULT 0,
    max_attempts   INT NOT NULL DEFAULT 5,
    last_error     TEXT,
    leased_until   TIMESTAMPTZ,
    leased_by      TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at   TIMESTAMPTZ,
    CONSTRAINT onboarding_tasks_status_check
        CHECK (status IN ('pending', 'running', 'completed', 'failed', 'blocked')),
    CONSTRAINT onboarding_tasks_step_check
        CHECK (step IN ('domain_verification', 'dns_propagation', 'certificate_issue', 'mailbox_provision', 'ready'))
);

CREATE INDEX ON onboarding_tasks (status, leased_until);
CREATE INDEX ON onboarding_tasks (tenant_id, created_at DESC);

ALTER TABLE onboarding_tasks ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON onboarding_tasks
    USING (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    WITH CHECK (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint);

GRANT SELECT, INSERT, UPDATE, DELETE ON onboarding_tasks TO urmail_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON onboarding_tasks TO urmail_platform;

CREATE TABLE delivery_events (
    id               BIGSERIAL PRIMARY KEY,
    tenant_id        BIGINT REFERENCES tenants(id) ON DELETE CASCADE,
    -- Postfix queue IDs are only unique within one Postfix instance, so the
    -- instance name is part of the identity of a delivery event. A collector
    -- must never resolve a tenant from queue_id alone.
    instance         TEXT NOT NULL DEFAULT '',
    queue_id         TEXT NOT NULL,
    message_id       TEXT,
    -- smtp/bounce/cleanup lines carry no sender: an empty value means the
    -- event could not be attributed to a sender, not that the row is invalid.
    sender           TEXT NOT NULL DEFAULT '',
    sender_domain    TEXT NOT NULL DEFAULT '',
    -- recipient/recipient_domain are nullable: queued/unknown events carry no recipient.
    recipient        TEXT,
    recipient_domain TEXT,
    relay            TEXT,
    status           TEXT NOT NULL,
    dsn              TEXT,
    delay_seconds    DOUBLE PRECISION NOT NULL DEFAULT 0,
    response_detail  TEXT,
    occurred_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT delivery_events_status_check
        CHECK (status IN ('queued', 'sent', 'deferred', 'bounced', 'expired', 'removed', 'unknown'))
);

CREATE INDEX ON delivery_events (queue_id);
-- Dedup guard: a replayed syslog line for the same queue message must not be
-- counted twice, and a state transition (deferred -> sent) must not be
-- swallowed by the earlier row. The key therefore includes the instance name
-- (queue IDs are per-instance), the recipient and the status; recipient is
-- COALESCEd so queued/unknown events (NULL recipient) still participate.
-- occurred_at is part of the key, so the collector must derive it from the
-- log line itself and never from the wall clock, or replays double-count.
CREATE UNIQUE INDEX delivery_events_dedupe_key
    ON delivery_events (tenant_id, instance, queue_id, COALESCE(recipient, ''), occurred_at, status);
CREATE INDEX ON delivery_events (tenant_id, occurred_at DESC);
CREATE INDEX ON delivery_events (sender_domain, occurred_at DESC);
CREATE INDEX ON delivery_events (status, occurred_at DESC);

ALTER TABLE delivery_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON delivery_events
    USING (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    WITH CHECK (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint);

GRANT SELECT, INSERT, UPDATE, DELETE ON delivery_events TO urmail_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON delivery_events TO urmail_platform;
-- Align with 0001: the platform role owns every table so it holds the full
-- privilege set (including TRUNCATE) and bypasses the tenant RLS policies.
ALTER TABLE onboarding_tasks OWNER TO urmail_platform;
ALTER TABLE delivery_events OWNER TO urmail_platform;

