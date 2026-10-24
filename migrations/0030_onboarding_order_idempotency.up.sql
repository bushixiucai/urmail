-- Migration 0030: one durable onboarding workflow per paid order.
-- Settlement retries and provider event replay may enqueue repeatedly; this
-- partial unique index makes the outbox-style dispatch idempotent.
CREATE UNIQUE INDEX onboarding_tasks_order_id_unique
    ON onboarding_tasks (tenant_id, order_id)
    WHERE order_id IS NOT NULL;

-- Workers claim queued work with SKIP LOCKED under the platform role. This
-- index keeps ready tasks cheap to find without coupling the queue to a process.
CREATE INDEX onboarding_tasks_claimable_idx
    ON onboarding_tasks (created_at, id)
    WHERE status IN ('pending', 'running');

ALTER TABLE onboarding_tasks
    ADD CONSTRAINT onboarding_tasks_attempts_check
    CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts);
