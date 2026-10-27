DROP TABLE IF EXISTS brand_host_challenges;
ALTER TABLE brand_hosts
  DROP COLUMN IF EXISTS updated_at,
  DROP COLUMN IF EXISTS activated_at,
  DROP COLUMN IF EXISTS cert_issued_at,
  DROP COLUMN IF EXISTS verified_at,
  DROP COLUMN IF EXISTS failure_reason,
  DROP COLUMN IF EXISTS status;
