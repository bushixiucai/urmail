DROP TABLE delivery_queue_contexts;
DROP TABLE collector_checkpoints;
DROP INDEX delivery_events_lifecycle_lookup;
CREATE UNIQUE INDEX delivery_events_dedupe_key ON delivery_events
 (tenant_id,instance,queue_id,COALESCE(recipient,''),occurred_at,status);
DROP INDEX delivery_events_source_identity;
ALTER TABLE delivery_events DROP COLUMN lifecycle_id,DROP COLUMN source_event_id;
