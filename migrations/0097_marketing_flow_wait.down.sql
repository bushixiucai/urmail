-- The prior function bodies are restored by replaying migrations 0095 and 0096.
ALTER TABLE marketing_flow_positions DROP COLUMN IF EXISTS wait_until;
