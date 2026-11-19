-- Webmail v1 新表（v3.2 §8.2；QA S0-b 后口径：三表补 mailbox_id）
-- scheduled_sends 不新建表：mailbox_scheduled_sends（0076）扩列接调度器家族。

-- 迁移簿记表（106 表门之一；user_version 是迁移链的权威，
-- schema_migrations 仅为工具面 parity：version=已应用版本、dirty=事务标记）。
CREATE TABLE IF NOT EXISTS "schema_migrations" (
  "version" INTEGER PRIMARY KEY,
  "dirty" INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS "snoozes" (
  "id" INTEGER PRIMARY KEY AUTOINCREMENT,
  "tenant_id" INTEGER NOT NULL,
  "mailbox_id" INTEGER NOT NULL,
  "folder" TEXT NOT NULL,
  "uid" INTEGER NOT NULL,
  "uid_validity" INTEGER NOT NULL,
  "snooze_until" INTEGER NOT NULL,
  "status" TEXT NOT NULL DEFAULT 'pending',
  "created_at" INTEGER NOT NULL,
  "updated_at" INTEGER NOT NULL,
  UNIQUE ("mailbox_id", "uid_validity", "uid")
);

CREATE TABLE IF NOT EXISTS "labels" (
  "id" INTEGER PRIMARY KEY AUTOINCREMENT,
  "tenant_id" INTEGER NOT NULL,
  "mailbox_id" INTEGER NOT NULL,
  "name" TEXT NOT NULL,
  "color_idx" INTEGER NOT NULL,
  "sort" INTEGER NOT NULL DEFAULT 0,
  "created_at" INTEGER NOT NULL,
  "updated_at" INTEGER NOT NULL,
  UNIQUE ("mailbox_id", "name")
);

-- 复合身份（v3.2 §8.1 条款）：无单一稳定 message 主键（maildir 无跨 MOVE
-- 对象 ID），以 (mailbox, folder, uid_validity, uid) 为行身份 + fingerprint
-- 用于外部 MOVE 后的对账恢复。
CREATE TABLE IF NOT EXISTS "message_labels" (
  "tenant_id" INTEGER NOT NULL,
  "mailbox_id" INTEGER NOT NULL,
  "folder" TEXT NOT NULL,
  "uid_validity" INTEGER NOT NULL,
  "uid" INTEGER NOT NULL,
  "label_id" INTEGER NOT NULL,
  "fingerprint" TEXT NOT NULL DEFAULT '',
  "orphan_since" INTEGER,
  PRIMARY KEY ("mailbox_id", "folder", "uid_validity", "uid", "label_id")
);

-- mailbox_scheduled_sends 接入 agent 调度器家族的扩列（0076 原复合键
-- (tenant,mailbox,folder,draft_uid,draft_uidvalidity) 不动，见 v3.2 §8.2）。
ALTER TABLE "mailbox_scheduled_sends" ADD COLUMN "scheduled_at" INTEGER;
ALTER TABLE "mailbox_scheduled_sends" ADD COLUMN "slot_tz" TEXT NOT NULL DEFAULT 'UTC';
ALTER TABLE "mailbox_scheduled_sends" ADD COLUMN "retry_count" INTEGER NOT NULL DEFAULT 0;
