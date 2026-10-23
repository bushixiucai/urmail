DROP POLICY settings_read ON settings;
DROP POLICY settings_insert ON settings;
DROP POLICY settings_update ON settings;
DROP POLICY settings_delete ON settings;
CREATE POLICY settings_isolation ON settings USING (scope='platform' OR (scope='brand' AND scope_id IN (SELECT brand_id FROM tenants WHERE id=nullif(current_setting('urmail.tenant_id',true),'')::bigint)) OR (scope='tenant' AND scope_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint)) WITH CHECK (scope='tenant' AND scope_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
GRANT INSERT,UPDATE,DELETE ON brands TO urmail_app;
