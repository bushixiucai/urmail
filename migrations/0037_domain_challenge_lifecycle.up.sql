-- Challenges have their own lifetime; an old domain can be verified again
-- without deleting its mailboxes or changing its creation timestamp.
ALTER TABLE domains
    ADD COLUMN verification_issued_at timestamptz,
    ADD COLUMN verification_expires_at timestamptz;
UPDATE domains SET verification_issued_at = created_at,
                   verification_expires_at = created_at + interval '7 days';
ALTER TABLE domains
    ALTER COLUMN verification_issued_at SET NOT NULL,
    ALTER COLUMN verification_issued_at SET DEFAULT now(),
    ALTER COLUMN verification_expires_at SET NOT NULL,
    ALTER COLUMN verification_expires_at SET DEFAULT (now() + interval '7 days'),
    ADD CONSTRAINT domains_challenge_lifetime CHECK (verification_expires_at > verification_issued_at);
