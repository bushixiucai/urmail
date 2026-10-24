-- Migration 0033 rollback: remove persisted marketing opt-outs. The guard
-- trigger and function go with the table; the function itself is dropped so
-- no orphan definition lingers.
DROP TABLE IF EXISTS marketing_suppressions;
DROP FUNCTION IF EXISTS urmail_suppression_no_weaken();
