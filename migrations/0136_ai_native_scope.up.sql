-- Version the exact native reading, authoring and retrieval export scope.
-- The consent guard rotates configuration_digest; previous approvals remain
-- historical evidence and cannot authorize this target, including on rollback.
ALTER TABLE ai_provider_configs ALTER COLUMN cloud_scope_hash SET DEFAULT 'e2c6e77cf242166013d200a355f30a3c18f2798ae245d891d58c50ad8a57ab82';
UPDATE ai_provider_configs SET cloud_scope_hash='e2c6e77cf242166013d200a355f30a3c18f2798ae245d891d58c50ad8a57ab82' WHERE cloud_scope_hash='43a8dfa67f89e1fa6861bf0f3c283bce46f112511c472b36e3174e30d1b037fe';
