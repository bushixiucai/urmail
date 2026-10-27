-- Marketing automation state.
-- This stores the tenant-authored flow and each recipient position.
-- It does not send mail. Unsubscribe is terminal for the flow position.

CREATE TABLE marketing_flows (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
 status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','paused')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (tenant_id, id)
);
CREATE TABLE marketing_flow_steps (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 flow_id BIGINT NOT NULL,
 step_key TEXT NOT NULL CHECK (length(step_key) BETWEEN 1 AND 40),
 kind TEXT NOT NULL CHECK (kind IN ('send','wait','stop')),
 next_key TEXT NOT NULL DEFAULT '',
 opened_key TEXT NOT NULL DEFAULT '',
 unsubscribed_key TEXT NOT NULL DEFAULT '',
 position INT NOT NULL CHECK (position BETWEEN 0 AND 19),
 UNIQUE (tenant_id, flow_id, step_key),
 UNIQUE (tenant_id, id),
 FOREIGN KEY (tenant_id, flow_id) REFERENCES marketing_flows(tenant_id, id) ON DELETE CASCADE
);
CREATE TABLE marketing_flow_positions (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 flow_id BIGINT NOT NULL,
 email TEXT NOT NULL CHECK (position('@' IN email) > 1),
 step_key TEXT NOT NULL,
 unsubscribed BOOLEAN NOT NULL DEFAULT false,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (tenant_id, flow_id, email),
 FOREIGN KEY (tenant_id, flow_id) REFERENCES marketing_flows(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE marketing_flows ENABLE ROW LEVEL SECURITY;
ALTER TABLE marketing_flows FORCE ROW LEVEL SECURITY;
ALTER TABLE marketing_flow_steps ENABLE ROW LEVEL SECURITY;
ALTER TABLE marketing_flow_steps FORCE ROW LEVEL SECURITY;
ALTER TABLE marketing_flow_positions ENABLE ROW LEVEL SECURITY;
ALTER TABLE marketing_flow_positions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON marketing_flows USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint) WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint);
CREATE POLICY tenant_isolation ON marketing_flow_steps USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint) WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint);
CREATE POLICY tenant_isolation ON marketing_flow_positions USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint) WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint);
GRANT SELECT, INSERT, UPDATE, DELETE ON marketing_flows, marketing_flow_steps, marketing_flow_positions TO urmail_app, urmail_platform;

-- The platform runtime role owns maintenance tables (restore quarantine,
-- fixture maintenance, TRUNCATE); same contract as 0028.
ALTER TABLE marketing_flows OWNER TO urmail_platform;
ALTER TABLE marketing_flow_steps OWNER TO urmail_platform;
ALTER TABLE marketing_flow_positions OWNER TO urmail_platform;
