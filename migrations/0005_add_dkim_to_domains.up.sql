-- DKIM keys must survive: the DNS TXT record published at creation time
-- only works while the platform keeps the matching private key.
ALTER TABLE domains
    ADD COLUMN dkim_selector TEXT NOT NULL DEFAULT 'mail',
    ADD COLUMN dkim_private_key TEXT;
