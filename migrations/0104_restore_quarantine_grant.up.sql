-- Allow the platform role to maintain the singleton quarantine row.
-- Ordinary application roles still have no privileges.

GRANT SELECT, INSERT, UPDATE ON restore_quarantine TO urmail_platform;
