ALTER TABLE domains
    DROP COLUMN IF EXISTS dkim_private_key,
    DROP COLUMN IF EXISTS dkim_selector;
