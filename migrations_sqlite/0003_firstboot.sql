-- 0003_firstboot.sql — compose first-boot bookkeeping (Phase 2).
-- Stores one-time bootstrap state (DKIM selector/public key, admin bootstrap
-- token hash).  No tenant data: the platform tenant is created by onboarding.
CREATE TABLE IF NOT EXISTS "firstboot_state" (
  "key" TEXT PRIMARY KEY,
  "value" TEXT NOT NULL,
  "created_at" INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)*1000000),
  "updated_at" INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)*1000000)
);

PRAGMA user_version = 3;
