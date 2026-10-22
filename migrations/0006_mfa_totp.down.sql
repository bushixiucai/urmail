-- Revert 0006: drop the recovery codes and restore the legacy BYTEA
-- secret column. convert_to re-encodes the text back to bytes losslessly.

ALTER TABLE users
    DROP COLUMN mfa_recovery_codes,
    ALTER COLUMN mfa_secret TYPE BYTEA USING convert_to(mfa_secret, 'UTF8');
