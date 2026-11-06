-- Commercial tables are not recreated. Rolling this version back leaves them
-- dropped; applying 0156 again is a no-op because every drop uses IF EXISTS.
-- TestMigrationIsReversible only executes the newest down (0157).
SELECT 1;
