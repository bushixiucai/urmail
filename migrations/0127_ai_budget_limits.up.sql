-- Tenant-level daily and monthly AI usage budgets (AI-005).
-- Zero means unlimited; platform plan limits still apply independently.
ALTER TABLE ai_provider_configs
 ADD COLUMN daily_token_limit bigint NOT NULL DEFAULT 0 CHECK(daily_token_limit BETWEEN 0 AND 9007199254740991),
 ADD COLUMN daily_cost_micros bigint NOT NULL DEFAULT 0 CHECK(daily_cost_micros BETWEEN 0 AND 9007199254740991),
 ADD COLUMN monthly_token_limit bigint NOT NULL DEFAULT 0 CHECK(monthly_token_limit BETWEEN 0 AND 9007199254740991),
 ADD COLUMN monthly_cost_micros bigint NOT NULL DEFAULT 0 CHECK(monthly_cost_micros BETWEEN 0 AND 9007199254740991);
