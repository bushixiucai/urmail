-- Source identity is independent of tenant attribution, including unknown
-- (NULL tenant) events. A checkpoint commits with its complete event batch.
ALTER TABLE delivery_events
 ADD COLUMN source_event_id text NOT NULL DEFAULT ('legacy:' || gen_random_uuid()::text),
 ADD COLUMN lifecycle_id text NOT NULL DEFAULT 'legacy';
CREATE UNIQUE INDEX delivery_events_source_identity ON delivery_events(source_event_id);
DROP INDEX delivery_events_dedupe_key;
CREATE INDEX delivery_events_lifecycle_lookup ON delivery_events(instance,queue_id,lifecycle_id,occurred_at);
CREATE TABLE collector_checkpoints (
 stream text PRIMARY KEY,
 source_identity text NOT NULL DEFAULT '',
 generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0),
 byte_offset bigint NOT NULL DEFAULT 0 CHECK(byte_offset>=0),
 gap_reason text,
 observed_at timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE delivery_queue_contexts (
 stream text NOT NULL REFERENCES collector_checkpoints(stream) ON DELETE CASCADE,
 instance text NOT NULL,
 queue_id text NOT NULL,
 lifecycle_id text NOT NULL,
 message_id text NOT NULL DEFAULT '',
 authenticated_mailbox text NOT NULL DEFAULT '',
 sender text NOT NULL DEFAULT '',
 started_at timestamptz NOT NULL,
 closed boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(stream,instance,queue_id)
);
ALTER TABLE collector_checkpoints OWNER TO urmail_platform;
ALTER TABLE delivery_queue_contexts OWNER TO urmail_platform;
ALTER TABLE collector_checkpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_queue_contexts ENABLE ROW LEVEL SECURITY;
CREATE POLICY collector_platform_only ON collector_checkpoints USING(false);
CREATE POLICY queue_context_platform_only ON delivery_queue_contexts USING(false);
REVOKE ALL ON collector_checkpoints,delivery_queue_contexts FROM PUBLIC,urmail_app,urmail_dovecot;
