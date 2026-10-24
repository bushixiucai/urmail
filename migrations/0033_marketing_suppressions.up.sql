-- Migration 0034: persist tenant-specific marketing opt-outs across restarts.
CREATE TABLE marketing_suppressions (
    id BIGSERIAL PRIMARY KEY,
    tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT 'manual',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT marketing_suppressions_email_normalized CHECK (email = lower(btrim(email)) AND email <> ''),
    CONSTRAINT marketing_suppressions_reason_check CHECK (reason IN ('unsubscribe', 'bounce', 'complaint', 'manual'))
);
CREATE UNIQUE INDEX marketing_suppressions_tenant_email_idx ON marketing_suppressions(tenant_id, email);
ALTER TABLE marketing_suppressions ENABLE ROW LEVEL SECURITY;
ALTER TABLE marketing_suppressions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON marketing_suppressions
    USING (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    WITH CHECK (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint);
GRANT SELECT, INSERT, DELETE ON marketing_suppressions TO urmail_app;
GRANT SELECT, INSERT, DELETE ON marketing_suppressions TO urmail_platform;
-- 0001 granted every later table the full arwd set to urmail_app through
-- ALTER DEFAULT PRIVILEGES. UPDATE is required for the upsert's conflict arm,
-- so instead of revoking it (which would break SubscribeOrUpdate) a guard
-- trigger below makes the consent NON-WEAKENABLE at the database level: the
-- reason may only move UP the precedence ladder, and the business key and
-- creation time are immutable.
GRANT UPDATE ON marketing_suppressions TO urmail_app;

CREATE FUNCTION urmail_suppression_no_weaken() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public AS $$
BEGIN
    IF NEW.tenant_id <> OLD.tenant_id OR NEW.email <> OLD.email
       OR NEW.created_at <> OLD.created_at OR NEW.id <> OLD.id THEN
        RAISE EXCEPTION 'SUPPRESSION_IDENTITY_IMMUTABLE' USING ERRCODE='23514';
    END IF;
    -- Precedence: unsubscribe/complaint/bounce survive as themselves; only a
    -- manual entry may be re-tagged, and a strong reason may never decay to
    -- manual (M5 review 3.14).
    IF OLD.reason <> 'manual' AND NEW.reason <> OLD.reason THEN
        RAISE EXCEPTION 'SUPPRESSION_REASON_IMMUTABLE' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END
$$;
ALTER FUNCTION urmail_suppression_no_weaken() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_suppression_no_weaken() FROM PUBLIC;

CREATE TRIGGER marketing_suppressions_no_weaken
    BEFORE UPDATE ON marketing_suppressions
    FOR EACH ROW EXECUTE FUNCTION urmail_suppression_no_weaken();
GRANT USAGE, SELECT ON SEQUENCE marketing_suppressions_id_seq TO urmail_app;
GRANT USAGE, SELECT ON SEQUENCE marketing_suppressions_id_seq TO urmail_platform;
ALTER TABLE marketing_suppressions OWNER TO urmail_platform;
