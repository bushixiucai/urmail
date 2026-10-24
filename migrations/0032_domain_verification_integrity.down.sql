-- Migration 0032 down
ALTER TABLE domains DROP CONSTRAINT IF EXISTS domains_active_requires_verified_at;
-- Demoted rows are not resurrected automatically: an operator who needs the
-- old rows back has to re-verify them, which is the point of the constraint.
