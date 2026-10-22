-- MFA (TOTP) support, design §18 M4.
--
-- users.mfa_secret already exists as BYTEA (0001) but was never used; it is
-- converted to TEXT so it can hold the base32 TOTP secret directly.
-- mfa_recovery_codes holds the SHA-256 hex hashes of the one-time recovery
-- codes as a JSONB array of strings.

ALTER TABLE users
    ALTER COLUMN mfa_secret TYPE TEXT USING encode(mfa_secret, 'escape'),
    ADD COLUMN mfa_recovery_codes JSONB NOT NULL DEFAULT '[]'::jsonb;
