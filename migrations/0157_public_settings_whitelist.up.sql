-- Public settings readers no longer receive the billing document profile.
DROP POLICY IF EXISTS settings_public_read ON settings;
CREATE POLICY settings_public_read ON settings AS RESTRICTIVE FOR SELECT
 USING(key IN('locale','theme','mailbox_default_quota_bytes','business_timezone'));
