ALTER TABLE domains DROP CONSTRAINT domains_challenge_lifetime,
    DROP COLUMN verification_expires_at,
    DROP COLUMN verification_issued_at;
