-- 0001_init: initial urMail.cn schema.
--
-- Design notes:
--   * PostgreSQL is the single source of truth. Dovecot, Postfix and Roundcube
--     all read the same tables.
--   * Tenant isolation is enforced twice: once by row-level security (the
--     authoritative boundary) and once by always including tenant_id in
--     queries (the ergonomic boundary).
--   * The RLS policies read the current tenant from the `urmail.tenant_id`
--     setting, which the application sets with SET LOCAL at the start of every
--     tenant-scoped transaction. When the setting is absent the comparison
--     evaluates to NULL and no rows are visible, so a missing context fails
--     closed rather than open.
--
-- The `urmail_app` role is subject to RLS. The `urmail_platform` role owns the
-- tables and therefore bypasses RLS; it is only used by platform endpoints.

-- ---------------------------------------------------------------------------
-- Roles
-- ---------------------------------------------------------------------------

-- Cluster-level roles are created by installer/bootstrap.sql, which runs as
-- a PostgreSQL superuser before the versioned migrations. Keeping them out of
-- 0001 lets the schema migrations run as the non-superuser urmail_migrate role.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'urmail_app')
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'urmail_platform')
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'urmail_dovecot')
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'urmail_migrate') THEN
        RAISE EXCEPTION 'urMail cluster roles are missing; run installer/bootstrap.sql first';
    END IF;
END
$$;

-- urmail_platform owns the tables and bypasses RLS; installer/bootstrap.sql
-- sets BYPASSRLS on the role. urmail_migrate is a member of urmail_platform so
-- the ownership transfer below is allowed.

-- ---------------------------------------------------------------------------
-- Brands, tenants and users
-- ---------------------------------------------------------------------------

CREATE TABLE brands (
    id           BIGSERIAL PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    settings     JSONB NOT NULL DEFAULT '{}'::jsonb,
    theme        JSONB NOT NULL DEFAULT '{}'::jsonb,
    logo_url     TEXT,
    favicon_url  TEXT,
    primary_color TEXT,
    status       TEXT NOT NULL DEFAULT 'active',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tenants (
    id            BIGSERIAL PRIMARY KEY,
    brand_id      BIGINT NOT NULL REFERENCES brands(id) ON DELETE RESTRICT,
    name          TEXT NOT NULL,
    slug          TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    owner_user_id BIGINT,
    settings      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (brand_id, slug),
    CONSTRAINT tenants_status_check
        CHECK (status IN ('pending', 'active', 'suspended', 'closed'))
);

CREATE TABLE users (
    id                   BIGSERIAL PRIMARY KEY,
    email                TEXT NOT NULL UNIQUE,
    password_hash        TEXT NOT NULL,
    display_name         TEXT,
    locale               TEXT NOT NULL DEFAULT 'zh-CN',
    timezone             TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    status               TEXT NOT NULL DEFAULT 'active',
    mfa_secret           BYTEA,
    mfa_enabled          BOOLEAN NOT NULL DEFAULT false,
    password_changed_at  TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT users_status_check
        CHECK (status IN ('active', 'disabled', 'locked'))
);

ALTER TABLE tenants
    ADD CONSTRAINT tenants_owner_fk
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE SET NULL;

CREATE TABLE platform_admins (
    user_id    BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_tenant_roles (
    user_id   BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    role      TEXT NOT NULL,
    PRIMARY KEY (user_id, tenant_id),
    CONSTRAINT user_tenant_roles_role_check
        CHECK (role IN ('owner', 'admin', 'billing', 'member'))
);

-- Settings are layered: platform (scope_id 0) < brand < tenant.
CREATE TABLE settings (
    scope      TEXT NOT NULL,
    scope_id   BIGINT NOT NULL DEFAULT 0,
    key        TEXT NOT NULL,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, scope_id, key),
    CONSTRAINT settings_scope_check
        CHECK (scope IN ('platform', 'brand', 'tenant'))
);

CREATE INDEX ON settings (scope, scope_id);

-- ---------------------------------------------------------------------------
-- Domains and mailboxes
-- ---------------------------------------------------------------------------

CREATE TABLE domains (
    id                 BIGSERIAL PRIMARY KEY,
    tenant_id          BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    fqdn               TEXT NOT NULL UNIQUE,
    mode               TEXT NOT NULL DEFAULT 'delegated',
    status             TEXT NOT NULL DEFAULT 'pending_dns',
    dns_provider       TEXT,
    provider_ref       TEXT,
    verification_token TEXT NOT NULL,
    verified_at        TIMESTAMPTZ,
    mx_host            TEXT,
    tls_mode           TEXT NOT NULL DEFAULT 'letsencrypt',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT domains_mode_check
        CHECK (mode IN ('managed', 'delegated')),
    CONSTRAINT domains_status_check
        CHECK (status IN ('pending_dns', 'active', 'failed', 'disabled'))
);

CREATE INDEX ON domains (tenant_id, status);

CREATE TABLE mailboxes (
    id            BIGSERIAL PRIMARY KEY,
    tenant_id     BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    domain_id     BIGINT NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    local_part    TEXT NOT NULL,
    address       TEXT NOT NULL UNIQUE,
    display_name  TEXT,
    password_hash TEXT NOT NULL,
    quota_bytes   BIGINT NOT NULL DEFAULT 0,
    status        TEXT NOT NULL DEFAULT 'active',
    send_enabled  BOOLEAN NOT NULL DEFAULT true,
    sieve_script  TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (domain_id, local_part),
    CONSTRAINT mailboxes_status_check
        CHECK (status IN ('active', 'disabled', 'locked'))
);

CREATE INDEX ON mailboxes (tenant_id, status);

CREATE TABLE mailbox_aliases (
    id         BIGSERIAL PRIMARY KEY,
    tenant_id  BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    domain_id  BIGINT NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    local_part TEXT NOT NULL,
    target     TEXT NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'alias',
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT mailbox_aliases_kind_check
        CHECK (kind IN ('alias', 'catchall', 'forward'))
);

-- An address may carry one ordinary alias, and a domain may carry one
-- catch-all. Encoding both as partial unique indexes keeps "postmaster" from
-- existing twice under different kinds.
CREATE UNIQUE INDEX mailbox_aliases_address_uniq
    ON mailbox_aliases (domain_id, local_part)
    WHERE kind <> 'catchall';
CREATE UNIQUE INDEX mailbox_aliases_catchall_uniq
    ON mailbox_aliases (domain_id)
    WHERE kind = 'catchall';
CREATE INDEX ON mailbox_aliases (tenant_id);

CREATE TABLE mailbox_usage (
    mailbox_id    BIGINT NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    tenant_id     BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    day           DATE NOT NULL,
    bytes_used    BIGINT NOT NULL DEFAULT 0,
    messages_sent INT NOT NULL DEFAULT 0,
    messages_recv INT NOT NULL DEFAULT 0,
    PRIMARY KEY (mailbox_id, day)
);

CREATE INDEX ON mailbox_usage (tenant_id, day);
CREATE INDEX ON mailbox_usage (day);

-- ---------------------------------------------------------------------------
-- Plans, subscriptions and billing
-- ---------------------------------------------------------------------------

CREATE TABLE plans (
    code             TEXT PRIMARY KEY,
    name             JSONB NOT NULL,
    description      JSONB NOT NULL DEFAULT '{}'::jsonb,
    price_cents      BIGINT NOT NULL,
    currency         TEXT NOT NULL DEFAULT 'CNY',
    quota_bytes      BIGINT NOT NULL,
    mailbox_limit    INT NOT NULL,
    domain_limit     INT NOT NULL,
    daily_send_limit INT NOT NULL,
    features         JSONB NOT NULL DEFAULT '{}'::jsonb,
    sort_order       INT NOT NULL DEFAULT 0,
    active           BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE subscriptions (
    id                   BIGSERIAL PRIMARY KEY,
    tenant_id            BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    plan_id              TEXT NOT NULL REFERENCES plans(code),
    status               TEXT NOT NULL,
    period_start         TIMESTAMPTZ NOT NULL,
    period_end           TIMESTAMPTZ NOT NULL,
    cancel_at_period_end BOOLEAN NOT NULL DEFAULT false,
    trial_ends_at        TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT subscriptions_status_check
        CHECK (status IN ('trialing', 'active', 'past_due', 'canceled', 'expired'))
);

CREATE INDEX ON subscriptions (tenant_id, status);
CREATE UNIQUE INDEX subscriptions_one_active_per_tenant
    ON subscriptions (tenant_id)
    WHERE status IN ('trialing', 'active', 'past_due');

CREATE TABLE channels (
    id             BIGSERIAL PRIMARY KEY,
    brand_id       BIGINT REFERENCES brands(id) ON DELETE CASCADE,
    code           TEXT NOT NULL UNIQUE,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL DEFAULT 'agent',
    commission_bps INT NOT NULL DEFAULT 0,
    parent_id      BIGINT REFERENCES channels(id) ON DELETE SET NULL,
    contact        JSONB NOT NULL DEFAULT '{}'::jsonb,
    active         BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT channels_kind_check
        CHECK (kind IN ('self', 'agent', 'reseller')),
    CONSTRAINT channels_commission_check
        CHECK (commission_bps BETWEEN 0 AND 10000)
);

CREATE TABLE channel_attribution (
    tenant_id  BIGINT PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    channel_id BIGINT NOT NULL REFERENCES channels(id) ON DELETE RESTRICT,
    ref_code   TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE orders (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       BIGINT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    order_no        TEXT NOT NULL UNIQUE,
    kind            TEXT NOT NULL,
    status          TEXT NOT NULL,
    amount_cents    BIGINT NOT NULL,
    currency        TEXT NOT NULL,
    plan_code       TEXT,
    channel_id      BIGINT REFERENCES channels(id) ON DELETE SET NULL,
    idempotency_key TEXT UNIQUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at         TIMESTAMPTZ,
    CONSTRAINT orders_kind_check
        CHECK (kind IN ('new', 'renew', 'upgrade', 'downgrade', 'manual')),
    CONSTRAINT orders_status_check
        CHECK (status IN ('pending', 'paid', 'failed', 'refunded',
                          'partially_refunded', 'canceled'))
);

CREATE INDEX ON orders (tenant_id, created_at DESC);
CREATE INDEX ON orders (channel_id, status);

CREATE TABLE order_items (
    id                BIGSERIAL PRIMARY KEY,
    order_id          BIGINT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    tenant_id         BIGINT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    kind              TEXT NOT NULL,
    plan_code         TEXT,
    quantity          INT NOT NULL DEFAULT 1,
    unit_price_cents  BIGINT NOT NULL,
    amount_cents      BIGINT NOT NULL,
    meta              JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE payments (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       BIGINT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    order_id        BIGINT REFERENCES orders(id) ON DELETE SET NULL,
    provider        TEXT NOT NULL,
    provider_txn_id TEXT,
    amount_cents    BIGINT NOT NULL,
    currency        TEXT NOT NULL,
    status          TEXT NOT NULL,
    raw             JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at         TIMESTAMPTZ,
    CONSTRAINT payments_status_check
        CHECK (status IN ('pending', 'succeeded', 'failed', 'refunded'))
);

CREATE UNIQUE INDEX payments_provider_txn_uniq
    ON payments (provider, provider_txn_id);
CREATE INDEX ON payments (tenant_id, created_at DESC);

CREATE TABLE payment_events (
    id                BIGSERIAL PRIMARY KEY,
    provider          TEXT NOT NULL,
    provider_event_id TEXT NOT NULL,
    payload           JSONB NOT NULL,
    processed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_event_id)
);

CREATE TABLE invoices (
    id           BIGSERIAL PRIMARY KEY,
    tenant_id    BIGINT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    number       TEXT NOT NULL UNIQUE,
    period_start TIMESTAMPTZ,
    period_end   TIMESTAMPTZ,
    amount_cents BIGINT NOT NULL,
    currency     TEXT NOT NULL,
    status       TEXT NOT NULL,
    pdf_path     TEXT,
    issued_at    TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT invoices_status_check
        CHECK (status IN ('draft', 'issued', 'paid', 'void'))
);

CREATE INDEX ON invoices (tenant_id, created_at DESC);

CREATE TABLE coupons (
    code            TEXT PRIMARY KEY,
    kind            TEXT NOT NULL,
    value           BIGINT NOT NULL,
    max_redemptions INT,
    redeemed_count  INT NOT NULL DEFAULT 0,
    valid_from      TIMESTAMPTZ,
    valid_to        TIMESTAMPTZ,
    active          BOOLEAN NOT NULL DEFAULT true,
    CONSTRAINT coupons_kind_check
        CHECK (kind IN ('percent', 'fixed', 'trial_days'))
);

CREATE TABLE coupon_redemptions (
    coupon_code TEXT NOT NULL REFERENCES coupons(code) ON DELETE CASCADE,
    tenant_id   BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    order_id    BIGINT REFERENCES orders(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (coupon_code, tenant_id)
);

CREATE TABLE commissions (
    id           BIGSERIAL PRIMARY KEY,
    channel_id   BIGINT NOT NULL REFERENCES channels(id) ON DELETE RESTRICT,
    tenant_id    BIGINT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    order_id     BIGINT NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    amount_cents BIGINT NOT NULL,
    bps          INT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT commissions_status_check
        CHECK (status IN ('pending', 'settled', 'void'))
);

-- ---------------------------------------------------------------------------
-- Compliance
-- ---------------------------------------------------------------------------

CREATE TABLE archive_entries (
    id          BIGSERIAL PRIMARY KEY,
    tenant_id   BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    mailbox_id  BIGINT REFERENCES mailboxes(id) ON DELETE SET NULL,
    message_id  TEXT NOT NULL,
    path        TEXT NOT NULL,
    subject     TEXT,
    from_addr   TEXT,
    to_addr     TEXT,
    received_at TIMESTAMPTZ NOT NULL,
    size_bytes  BIGINT NOT NULL DEFAULT 0,
    legal_hold  BOOLEAN NOT NULL DEFAULT false,
    deleted_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, message_id)
);

CREATE INDEX ON archive_entries (tenant_id, received_at DESC);
CREATE INDEX ON archive_entries USING gin (to_tsvector('simple', coalesce(subject, '')));

CREATE TABLE recall_requests (
    id           BIGSERIAL PRIMARY KEY,
    tenant_id    BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    message_id   TEXT NOT NULL,
    requested_by BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status       TEXT NOT NULL DEFAULT 'pending',
    reason       TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT recall_requests_status_check
        CHECK (status IN ('pending', 'delivered', 'failed', 'expired'))
);

CREATE TABLE audit_logs (
    id             BIGSERIAL PRIMARY KEY,
    tenant_id      BIGINT REFERENCES tenants(id) ON DELETE SET NULL,
    actor_user_id  BIGINT REFERENCES users(id) ON DELETE SET NULL,
    actor_ip       INET,
    action         TEXT NOT NULL,
    target_type    TEXT,
    target_id      TEXT,
    detail         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON audit_logs (tenant_id, created_at DESC);
CREATE INDEX ON audit_logs (action, created_at DESC);

-- ---------------------------------------------------------------------------
-- Support
-- ---------------------------------------------------------------------------

CREATE TABLE tickets (
    id         BIGSERIAL PRIMARY KEY,
    tenant_id  BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    opened_by  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subject    TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'open',
    priority   TEXT NOT NULL DEFAULT 'normal',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tickets_status_check
        CHECK (status IN ('open', 'pending', 'resolved', 'closed'))
);

CREATE TABLE ticket_messages (
    id         BIGSERIAL PRIMARY KEY,
    ticket_id  BIGINT NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    author_id  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    body       TEXT NOT NULL,
    is_staff   BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE announcements (
    id           BIGSERIAL PRIMARY KEY,
    brand_id     BIGINT REFERENCES brands(id) ON DELETE CASCADE,
    title        JSONB NOT NULL,
    body         JSONB NOT NULL,
    published_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- API tokens and sessions
-- ---------------------------------------------------------------------------

CREATE TABLE api_tokens (
    id           BIGSERIAL PRIMARY KEY,
    tenant_id    BIGINT REFERENCES tenants(id) ON DELETE CASCADE,
    user_id      BIGINT REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL UNIQUE,
    hash         TEXT NOT NULL,
    scopes       TEXT[] NOT NULL DEFAULT '{}',
    quota_limit  BIGINT,
    used_count   BIGINT NOT NULL DEFAULT 0,
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id         TEXT PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id  BIGINT REFERENCES tenants(id) ON DELETE CASCADE,
    csrf_token TEXT NOT NULL,
    user_agent TEXT,
    ip         INET,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON sessions (user_id);
CREATE INDEX ON sessions (expires_at);

-- ---------------------------------------------------------------------------
-- Row-level security
-- ---------------------------------------------------------------------------
--
-- Two session settings drive the policies:
--
--   urmail.tenant_id  the tenant the current request belongs to
--   urmail.user_id    the authenticated user, when there is one
--
-- The application sets both with SET LOCAL at the start of a transaction, so
-- they are discarded when the transaction ends and a pooled connection can
-- never carry one tenant's context into the next request. When a setting is
-- absent the comparison evaluates to NULL and no row is visible: a missing
-- context fails closed rather than open.
--
-- FORCE ROW LEVEL SECURITY is deliberately NOT used: the platform role owns
-- these tables and must see every tenant. The tenant-scoped role does not own
-- them, so the policies apply to it.

DO $$
DECLARE
    target TEXT;
    tables TEXT[] := ARRAY[
        'domains', 'mailboxes', 'mailbox_aliases', 'mailbox_usage',
        'subscriptions', 'orders', 'order_items', 'payments', 'invoices',
        'coupon_redemptions', 'channel_attribution', 'archive_entries',
        'recall_requests', 'commissions', 'user_tenant_roles'
    ];
BEGIN
    FOREACH target IN ARRAY tables LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', target);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (tenant_id = nullif(current_setting(''urmail.tenant_id'', true), '''')::bigint) WITH CHECK (tenant_id = nullif(current_setting(''urmail.tenant_id'', true), '''')::bigint)',
            target);
    END LOOP;
END
$$;

-- The tenant row itself is identified by its primary key.
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenants
    USING (id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    WITH CHECK (id = nullif(current_setting('urmail.tenant_id', true), '')::bigint);

-- A user can see themselves and the other users of their own tenant.
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
CREATE POLICY users_isolation ON users
    USING (
        id = nullif(current_setting('urmail.user_id', true), '')::bigint
        OR id IN (
            SELECT user_id FROM user_tenant_roles
            WHERE tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    );

-- Sessions and API tokens belong to a user and, optionally, to a tenant.
ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY sessions_isolation ON sessions
    USING (
        user_id = nullif(current_setting('urmail.user_id', true), '')::bigint
        OR tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    WITH CHECK (
        user_id = nullif(current_setting('urmail.user_id', true), '')::bigint);

ALTER TABLE api_tokens ENABLE ROW LEVEL SECURITY;
CREATE POLICY api_tokens_isolation ON api_tokens
    USING (
        user_id = nullif(current_setting('urmail.user_id', true), '')::bigint
        OR tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    WITH CHECK (
        user_id = nullif(current_setting('urmail.user_id', true), '')::bigint);

-- Ticket messages inherit the tenant of their ticket.
ALTER TABLE ticket_messages ENABLE ROW LEVEL SECURITY;
CREATE POLICY ticket_messages_isolation ON ticket_messages
    USING (ticket_id IN (
        SELECT id FROM tickets
        WHERE tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint));

-- A tenant sees the brands of its own tenants, and the channels of its brand.
ALTER TABLE brands ENABLE ROW LEVEL SECURITY;
CREATE POLICY brands_isolation ON brands
    USING (id IN (
        SELECT brand_id FROM tenants
        WHERE id = nullif(current_setting('urmail.tenant_id', true), '')::bigint));

ALTER TABLE channels ENABLE ROW LEVEL SECURITY;
CREATE POLICY channels_isolation ON channels
    USING (
        brand_id IS NULL
        OR brand_id IN (
            SELECT brand_id FROM tenants
            WHERE id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    );

-- Announcements are published per brand.
ALTER TABLE announcements ENABLE ROW LEVEL SECURITY;
CREATE POLICY announcements_isolation ON announcements
    USING (
        brand_id IS NULL
        OR brand_id IN (
            SELECT brand_id FROM tenants
            WHERE id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    );

-- Settings are layered: platform < brand < tenant.
ALTER TABLE settings ENABLE ROW LEVEL SECURITY;
CREATE POLICY settings_isolation ON settings
    USING (
        scope = 'platform'
        OR (scope = 'brand' AND scope_id IN (
            SELECT brand_id FROM tenants
            WHERE id = nullif(current_setting('urmail.tenant_id', true), '')::bigint))
        OR (scope = 'tenant' AND scope_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    )
    WITH CHECK (
        scope = 'platform'
        OR (scope = 'tenant' AND scope_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    );

-- Platform-only tables are invisible to the tenant-scoped role. The owner
-- (urmail_platform) bypasses the policies.
ALTER TABLE platform_admins ENABLE ROW LEVEL SECURITY;
CREATE POLICY platform_admins_isolation ON platform_admins USING (false);

ALTER TABLE payment_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY payment_events_isolation ON payment_events USING (false);

-- Ownership of every table belongs to the platform role. Owning the tables
-- grants it the full privilege set (including TRUNCATE, which the explicit
-- GRANT below does not cover) and, combined with BYPASSRLS, lets it see
-- across every tenant.
DO $$
DECLARE
    target RECORD;
BEGIN
    FOR target IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public' AND c.relkind = 'r'
    LOOP
        EXECUTE format('ALTER TABLE %I OWNER TO urmail_platform', target.relname);
    END LOOP;
END
$$;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO urmail_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO urmail_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO urmail_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO urmail_app;
