-- §3.5 trigram 降级升级（P2-3）：FTS5 trigram 分词器辅助表覆盖 4 个原 gin_trgm_ops
-- 检索面（0028: archive_entries.subject/from_addr/message_id + 0040: archive_content.body_text）。
-- 语义等价性：FTS5 trigram 分词 + 列上 LIKE 加速 == PG gin_trgm_ops 的 %q% 子串匹配。
-- 维护方式：行级触发器随 archive_entries / archive_content 写入同步（含迁移期回填）。

CREATE VIRTUAL TABLE archive_search_fts USING fts5(
  subject,
  from_addr,
  message_id,
  body_text,
  tokenize = 'trigram'
);

INSERT INTO archive_search_fts(rowid, subject, from_addr, message_id, body_text)
SELECT e.id, coalesce(e.subject,''), coalesce(e.from_addr,''), coalesce(e.message_id,''), coalesce(c.body_text,'')
FROM archive_entries e JOIN archive_content c ON c.entry_id=e.id AND c.tenant_id=e.tenant_id;

CREATE TRIGGER archive_search_fts_entries_ai
AFTER INSERT ON archive_entries BEGIN
  INSERT INTO archive_search_fts(rowid, subject, from_addr, message_id, body_text)
  VALUES (NEW.id, coalesce(NEW.subject,''), coalesce(NEW.from_addr,''), coalesce(NEW.message_id,''), '');
END;

CREATE TRIGGER archive_search_fts_entries_ad
AFTER DELETE ON archive_entries BEGIN
  DELETE FROM archive_search_fts WHERE rowid = OLD.id;
END;

CREATE TRIGGER archive_search_fts_entries_au
AFTER UPDATE OF subject, from_addr, message_id ON archive_entries BEGIN
  UPDATE archive_search_fts SET subject = coalesce(NEW.subject,''), from_addr = coalesce(NEW.from_addr,''),
    message_id = coalesce(NEW.message_id,'')
  WHERE rowid = NEW.id;
END;

CREATE TRIGGER archive_search_fts_content_ai
AFTER INSERT ON archive_content BEGIN
  UPDATE archive_search_fts SET body_text = coalesce(NEW.body_text,'')
  WHERE rowid = NEW.entry_id;
END;

CREATE TRIGGER archive_search_fts_content_au
AFTER UPDATE OF body_text ON archive_content BEGIN
  UPDATE archive_search_fts SET body_text = coalesce(NEW.body_text,'')
  WHERE rowid = NEW.entry_id;
END;

CREATE TRIGGER archive_search_fts_content_ad
AFTER DELETE ON archive_content BEGIN
  DELETE FROM archive_search_fts WHERE rowid = OLD.entry_id;
END;
