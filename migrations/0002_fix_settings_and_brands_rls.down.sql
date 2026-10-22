-- 0002_fix_settings_and_brands_rls.down.sql
--
-- Rollback 0002 to original 0001 policies.

DROP POLICY IF EXISTS settings_isolation ON settings;
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

DROP POLICY IF EXISTS brands_isolation ON brands;
CREATE POLICY brands_isolation ON brands
    USING (id IN (
        SELECT brand_id FROM tenants
        WHERE id = nullif(current_setting('urmail.tenant_id', true), '')::bigint));
