DROP POLICY settings_isolation ON settings;
CREATE POLICY settings_read ON settings FOR SELECT USING (
 scope = 'platform' OR (scope = 'brand' AND scope_id IN (SELECT brand_id FROM tenants WHERE id = nullif(current_setting('urmail.tenant_id',true),'')::bigint)) OR (scope='tenant' AND scope_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint));
CREATE POLICY settings_insert ON settings FOR INSERT WITH CHECK (scope='tenant' AND scope_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
CREATE POLICY settings_update ON settings FOR UPDATE USING (scope='tenant' AND scope_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint) WITH CHECK (scope='tenant' AND scope_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
CREATE POLICY settings_delete ON settings FOR DELETE USING (scope='tenant' AND scope_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
REVOKE INSERT, UPDATE, DELETE ON brands FROM urmail_app;
