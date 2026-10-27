-- OPS-001: automated encrypted offsite backup run state (Task OPS-001).
-- One row per scheduled backup attempt, in strict pipeline order:
-- running -> verifying -> success | failed. The table is platform-owned
-- operational state: tenants must never see backup topology or error text,
-- so the RLS policy denies every tenant context (same shape as
-- operational_alerts in migration 0045) and only the platform role,
-- which bypasses RLS as owner, reads and writes it.

CREATE TABLE backup_runs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 run_uid text NOT NULL UNIQUE CHECK(length(run_uid) BETWEEN 8 AND 64),
 state text NOT NULL CHECK(state IN('running','verifying','success','failed')),
 scope text NOT NULL DEFAULT '' CHECK(length(scope) <= 64),
 started_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz,
 bytes bigint NOT NULL DEFAULT 0 CHECK(bytes >= 0),
 duration_ms bigint NOT NULL DEFAULT 0 CHECK(duration_ms >= 0),
 error text NOT NULL DEFAULT '' CHECK(length(error) <= 2000),
 manifest_name text NOT NULL DEFAULT '' CHECK(length(manifest_name) <= 300),
 offsite_uri text NOT NULL DEFAULT '' CHECK(length(offsite_uri) <= 300),
 retention_deleted integer NOT NULL DEFAULT 0 CHECK(retention_deleted >= 0)
);
CREATE INDEX backup_runs_started_at_idx ON backup_runs(started_at DESC);

ALTER TABLE backup_runs OWNER TO urmail_platform;
REVOKE ALL ON backup_runs FROM PUBLIC, urmail_app, urmail_dovecot;
ALTER TABLE backup_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE backup_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY no_tenant_access ON backup_runs USING(false) WITH CHECK(false);
