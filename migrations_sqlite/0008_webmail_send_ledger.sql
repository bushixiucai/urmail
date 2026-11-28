-- Webmail send ledger + draft journal (pm-webmail-writepath-1).
-- Slice-runtime mirror of the PG platform send-receipts semantics
-- (migrations/0152_mailbox_send_receipts): exactly-once submission per
-- idempotency key, single live submission per source draft, claim-lease
-- submit protocol with lazy stale recovery, sent-copy reconciliation state.
CREATE TABLE IF NOT EXISTS mailbox_send_ledger (
  "id" INTEGER PRIMARY KEY,
  "tenant_id" INTEGER NOT NULL,
  "mailbox_id" INTEGER NOT NULL,
  "request_key" TEXT NOT NULL,
  "request_hash" TEXT NOT NULL,
  "message_id" TEXT NOT NULL DEFAULT '',
  "raw_secret" TEXT NOT NULL DEFAULT '',
  "smtp_secret" TEXT NOT NULL DEFAULT '',
  "smtp_password_version" TEXT NOT NULL DEFAULT '',
  "status" TEXT NOT NULL DEFAULT 'created'
    CHECK ("status" IN ('created','submitting','accepted','uncertain','rejected')),
  "claim_token" INTEGER NOT NULL DEFAULT 0,
  "lease_until" INTEGER NOT NULL DEFAULT 0,
  "sent_copy_state" TEXT NOT NULL DEFAULT 'none'
    CHECK ("sent_copy_state" IN ('none','pending','complete','held')),
  "copy_attempts" INTEGER NOT NULL DEFAULT 0,
  "copy_pending_since" INTEGER NOT NULL DEFAULT 0,
  "accepted_at" INTEGER NOT NULL DEFAULT 0,
  "last_error" TEXT NOT NULL DEFAULT '',
  "draft_folder" TEXT NOT NULL DEFAULT '',
  "draft_uid" INTEGER NOT NULL DEFAULT 0,
  "draft_uidvalidity" INTEGER NOT NULL DEFAULT 0,
  "draft_released" INTEGER NOT NULL DEFAULT 0,
  "next_attempt_at" INTEGER NOT NULL DEFAULT 0,
  "created_at" INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)*1000000),
  "updated_at" INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)*1000000)
);
CREATE UNIQUE INDEX IF NOT EXISTS "idx_send_ledger_request_key"
  ON "mailbox_send_ledger"("tenant_id","mailbox_id","request_key");
-- A source draft holds at most one live submission; a rejected send frees
-- the draft for another attempt (matches the PG draft-send lock contract).
CREATE UNIQUE INDEX IF NOT EXISTS "idx_send_ledger_draft_live"
  ON "mailbox_send_ledger"("tenant_id","mailbox_id","draft_folder","draft_uid","draft_uidvalidity")
  WHERE "draft_uid" > 0 AND "status" <> 'rejected';
CREATE INDEX IF NOT EXISTS "idx_send_ledger_owner"
  ON "mailbox_send_ledger"("tenant_id","mailbox_id","id");

-- Server-side draft saves: idempotent append journal keyed per mailbox.
-- result_json carries the webmail.DraftSaveResult payload (digest, uid,
-- uidvalidity, cleanup flags) exactly like the PG data-import receipt did.
CREATE TABLE IF NOT EXISTS "mailbox_draft_journal" (
  "id" INTEGER PRIMARY KEY,
  "tenant_id" INTEGER NOT NULL,
  "mailbox_id" INTEGER NOT NULL,
  "request_key" TEXT NOT NULL,
  "digest" TEXT NOT NULL DEFAULT '',
  "message_id" TEXT NOT NULL DEFAULT '',
  "status" TEXT NOT NULL DEFAULT 'claimed'
    CHECK ("status" IN ('claimed','done')),
  "result_json" TEXT NOT NULL DEFAULT '',
  "folder" TEXT NOT NULL DEFAULT '',
  "uid" INTEGER NOT NULL DEFAULT 0,
  "uidvalidity" INTEGER NOT NULL DEFAULT 0,
  "created_at" INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)*1000000)
);
CREATE UNIQUE INDEX IF NOT EXISTS "idx_draft_journal_key"
  ON "mailbox_draft_journal"("tenant_id","mailbox_id","request_key");
