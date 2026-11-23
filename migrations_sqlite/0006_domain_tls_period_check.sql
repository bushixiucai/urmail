-- Rebuild domain_tls_reports so existing databases receive the corrected
-- 8-day period CHECK. Fresh databases already get it from 0001; an upgraded
-- database keeps the old CHECK until this table is rebuilt. Rows, the table
-- UNIQUE constraint and the explicit unique index are preserved verbatim.
PRAGMA defer_foreign_keys=ON;
CREATE TABLE "domain_tls_reports_rebuilt" (
  "id" INTEGER PRIMARY KEY AUTOINCREMENT,
  "tenant_id" INTEGER NOT NULL,
  "domain_id" INTEGER NOT NULL,
  "report_digest" TEXT NOT NULL,
  "reporter_digest" TEXT NOT NULL,
  "report_id_digest" TEXT NOT NULL,
  "period_start" INTEGER NOT NULL,
  "period_end" INTEGER NOT NULL,
  "successful" INTEGER NOT NULL,
  "failed" INTEGER NOT NULL,
  "failures" TEXT NOT NULL,
  "received_at" INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)*1000000),
  "source_verified" INTEGER NOT NULL DEFAULT 0,
  UNIQUE (tenant_id, domain_id, reporter_digest, report_id_digest, period_start, period_end),
  FOREIGN KEY (tenant_id, domain_id) REFERENCES domains(tenant_id, id),
  FOREIGN KEY (tenant_id) REFERENCES tenants(id),
  CHECK (period_end >= period_start AND period_end <= (period_start + 691200000000)),
  CHECK (failed >= 0 AND failed <= 1000000000),
  CHECK (length(report_digest) = 64),
  CHECK (length(report_id_digest) = 64),
  CHECK (length(reporter_digest) = 64),
  CHECK (NOT source_verified),
  CHECK (successful >= 0 AND successful <= 1000000000)
);
INSERT INTO "domain_tls_reports_rebuilt" SELECT * FROM "domain_tls_reports";
DROP TABLE "domain_tls_reports";
ALTER TABLE "domain_tls_reports_rebuilt" RENAME TO "domain_tls_reports";
CREATE UNIQUE INDEX IF NOT EXISTS domain_tls_reports_tenant_id_domain_id_reporter_digest_repo_key ON domain_tls_reports (tenant_id, domain_id, reporter_digest, report_id_digest, period_start, period_end);
