-- 0002_fix_settings_and_brands_rls.up.sql
--
-- Security fixes from v1 design review:
-- 1. D2: settings RLS WITH CHECK allowed scope = 'platform' which allowed tenant-scoped
--    role to write platform-level settings. Restrict WITH CHECK to tenant's own scope.
-- 2. §3.4: brands table RLS lacked WITH CHECK, allowing tenants to insert new brands.
--    Add WITH CHECK (false) so tenant-scoped role cannot insert/update brands.

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
        scope = 'tenant' AND scope_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint
    );

DROP POLICY IF EXISTS brands_isolation ON brands;
CREATE POLICY brands_isolation ON brands
    USING (id IN (
        SELECT brand_id FROM tenants
        WHERE id = nullif(current_setting('urmail.tenant_id', true), '')::bigint))
    WITH CHECK (false);
