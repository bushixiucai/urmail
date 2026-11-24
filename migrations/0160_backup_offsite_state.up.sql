-- Offsite disposition of a backup run (P4-FB-02 local-only decision).
-- '' means historical/unknown: the fresh-backup gate treats it as NOT
-- satisfied. 'uploaded' requires a non-empty offsite_uri; 'pending_local_only'
-- is an explicit operator-approved local complete backup with offsite owed.
ALTER TABLE backup_runs ADD COLUMN offsite_state text NOT NULL DEFAULT ''
    CHECK(offsite_state IN('', 'uploaded', 'pending_local_only'));
