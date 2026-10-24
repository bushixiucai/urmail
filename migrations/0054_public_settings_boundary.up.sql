-- General settings readers must never receive private provider/relay material.
-- The separately authorized billing profile retains its existing 0052 guard.
CREATE POLICY settings_public_read ON settings AS RESTRICTIVE FOR SELECT
 USING(key IN('locale','theme','mailbox_default_quota_bytes','business_timezone','billing_document_profile'));
