-- Migration 0030 down
ALTER TABLE onboarding_tasks DROP CONSTRAINT onboarding_tasks_attempts_check;
DROP INDEX onboarding_tasks_claimable_idx;
DROP INDEX onboarding_tasks_order_id_unique;
