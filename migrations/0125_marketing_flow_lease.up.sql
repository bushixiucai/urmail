-- Claim leases for the marketing flow tick worker and wait-step durations.
-- The worker leases a due position so concurrent ticks cannot double-send;
-- the fence increments on every claim, mirroring the campaign recipient lease.
-- wait_seconds arms the per-recipient wait_until when a position lands on a
-- wait step; it stays NULL until the worker arms it.

ALTER TABLE marketing_flow_positions
 ADD COLUMN lease_owner TEXT,
 ADD COLUMN lease_until TIMESTAMPTZ,
 ADD COLUMN lease_fence BIGINT NOT NULL DEFAULT 0;

ALTER TABLE marketing_flow_steps
 ADD COLUMN wait_seconds INT NOT NULL DEFAULT 0 CHECK (wait_seconds BETWEEN 0 AND 2592000);
