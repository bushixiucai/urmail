DROP TABLE brand_hosts;
DROP TABLE application_credentials;
ALTER TABLE sessions DROP COLUMN mfa_verified_at, DROP COLUMN kind;
