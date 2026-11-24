-- Offsite disposition of a backup run (P4-FB-02 local-only decision).
-- Historical rows keep ''; the fresh-backup gate treats that as not satisfied.
ALTER TABLE backup_runs ADD COLUMN offsite_state TEXT NOT NULL DEFAULT '';
