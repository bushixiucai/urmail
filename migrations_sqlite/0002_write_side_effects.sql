-- 0002_write_side_effects.sql — S2 B1b：DML 副作用触发器族。
-- 语义来源：原 PG DML-CTE 的 logged/audit 第二写（mailbox_changes 日志、
-- audit_logs 平台审计）在 SQLite 用 AFTER 触发器等价承接——触发器与主 DML
-- 同事务触发，保持「写一行、记一笔」的原子性；repo 侧单语句即可完成。

-- mailbox_contacts → mailbox_changes
CREATE TRIGGER IF NOT EXISTS mailbox_contacts_changes_ins AFTER INSERT ON mailbox_contacts
BEGIN
  INSERT INTO mailbox_changes(tenant_id, mailbox_id, resource_type, resource_id, version, deleted)
  SELECT NEW.tenant_id, NEW.mailbox_id, 'contact', NEW.id, NEW.version, false;
END;
CREATE TRIGGER IF NOT EXISTS mailbox_contacts_changes_upd AFTER UPDATE ON mailbox_contacts
WHEN NEW.version > OLD.version
BEGIN
  INSERT INTO mailbox_changes(tenant_id, mailbox_id, resource_type, resource_id, version, deleted)
  SELECT NEW.tenant_id, NEW.mailbox_id, 'contact', NEW.id, NEW.version, NEW.deleted_at IS NOT NULL;
END;

-- mailbox_events → mailbox_changes
CREATE TRIGGER IF NOT EXISTS mailbox_events_changes_ins AFTER INSERT ON mailbox_events
BEGIN
  INSERT INTO mailbox_changes(tenant_id, mailbox_id, resource_type, resource_id, version, deleted)
  SELECT NEW.tenant_id, NEW.mailbox_id, 'event', NEW.id, NEW.version, false;
END;
CREATE TRIGGER IF NOT EXISTS mailbox_events_changes_upd AFTER UPDATE ON mailbox_events
WHEN NEW.version > OLD.version
BEGIN
  INSERT INTO mailbox_changes(tenant_id, mailbox_id, resource_type, resource_id, version, deleted)
  SELECT NEW.tenant_id, NEW.mailbox_id, 'event', NEW.id, NEW.version, NEW.deleted_at IS NOT NULL;
END;

-- application_credentials → audit_logs（create + 首次 revoke）
CREATE TRIGGER IF NOT EXISTS application_credentials_audit_create AFTER INSERT ON application_credentials
BEGIN
  INSERT INTO audit_logs(actor_user_id, action, target_type, target_id, detail)
  VALUES(NEW.user_id, 'application_credential.create', 'application_credential', CAST(NEW.id AS TEXT), '{}');
END;
CREATE TRIGGER IF NOT EXISTS application_credentials_audit_revoke AFTER UPDATE ON application_credentials
WHEN OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL
BEGIN
  INSERT INTO audit_logs(actor_user_id, action, target_type, target_id, detail)
  VALUES(NEW.user_id, 'application_credential.revoke', 'application_credential', CAST(NEW.id AS TEXT), '{}');
END;

-- recall_requests 完成审计（review 完成或直接 complete）
CREATE TRIGGER IF NOT EXISTS recall_requests_audit_complete AFTER UPDATE ON recall_requests
WHEN OLD.status = 'pending' AND NEW.status IN('local_recalled','failed','uncertain','expired')
 AND NEW.preview_actor_id IS NOT NULL
BEGIN
  INSERT INTO audit_logs(tenant_id, actor_user_id, action, target_type, target_id, detail)
  VALUES(NEW.tenant_id, COALESCE(NEW.preview_actor_id, NEW.requested_by),
         'compliance.recall.review_complete', 'recall', CAST(NEW.id AS TEXT),
         json_object('status', NEW.status, 'scope', 'local_mailbox', 'epoch', NEW.attempt_epoch,
                     'digest', NEW.preview_digest, 'selection', NEW.selected_copy_keys,
                     'result', NEW.local_result));
END;
CREATE TRIGGER IF NOT EXISTS recall_requests_audit_complete_direct AFTER UPDATE ON recall_requests
WHEN OLD.status = 'pending' AND NEW.status IN('local_recalled','failed','uncertain','expired')
 AND NEW.preview_actor_id IS NULL
BEGIN
  INSERT INTO audit_logs(tenant_id, actor_user_id, action, target_type, target_id, detail)
  VALUES(NEW.tenant_id, NEW.requested_by, 'compliance.recall.complete', 'recall', CAST(NEW.id AS TEXT),
         json_object('status', NEW.status, 'scope', 'local_mailbox'));
END;
