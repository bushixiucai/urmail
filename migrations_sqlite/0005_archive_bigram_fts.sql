-- §3.5 trigram 降级双字符补充（P2-3）：FTS5 ascii + urmail_bigrams 辅助表
-- 覆盖 4 个检索面（subject/from_addr/message_id/body_text）的 2-rune 子串检索，
-- 解决 FTS5 trigram 对 2 字符（如双字中文）不可索引导致全扫超 500ms 门槛的问题。

CREATE VIRTUAL TABLE archive_search_bigram_fts USING fts5(
  subject,
  from_addr,
  message_id,
  body_text,
  tokenize = 'ascii'
);

INSERT INTO archive_search_bigram_fts(rowid, subject, from_addr, message_id, body_text)
SELECT e.id,
  urmail_bigrams(coalesce(e.subject,'')),
  urmail_bigrams(coalesce(e.from_addr,'')),
  urmail_bigrams(coalesce(e.message_id,'')),
  urmail_bigrams(coalesce(c.body_text,''))
FROM archive_entries e JOIN archive_content c ON c.entry_id=e.id AND c.tenant_id=e.tenant_id;

CREATE TRIGGER archive_search_bigram_fts_entries_ai
AFTER INSERT ON archive_entries BEGIN
  INSERT INTO archive_search_bigram_fts(rowid, subject, from_addr, message_id, body_text)
  VALUES (NEW.id, urmail_bigrams(coalesce(NEW.subject,'')), urmail_bigrams(coalesce(NEW.from_addr,'')), urmail_bigrams(coalesce(NEW.message_id,'')), '');
END;

CREATE TRIGGER archive_search_bigram_fts_entries_ad
AFTER DELETE ON archive_entries BEGIN
  DELETE FROM archive_search_bigram_fts WHERE rowid = OLD.id;
END;

CREATE TRIGGER archive_search_bigram_fts_entries_au
AFTER UPDATE OF subject, from_addr, message_id ON archive_entries BEGIN
  UPDATE archive_search_bigram_fts SET
    subject = urmail_bigrams(coalesce(NEW.subject,'')),
    from_addr = urmail_bigrams(coalesce(NEW.from_addr,'')),
    message_id = urmail_bigrams(coalesce(NEW.message_id,''))
  WHERE rowid = NEW.id;
END;

CREATE TRIGGER archive_search_bigram_fts_content_ai
AFTER INSERT ON archive_content BEGIN
  UPDATE archive_search_bigram_fts SET body_text = urmail_bigrams(coalesce(NEW.body_text,''))
  WHERE rowid = NEW.entry_id;
END;

CREATE TRIGGER archive_search_bigram_fts_content_au
AFTER UPDATE OF body_text ON archive_content BEGIN
  UPDATE archive_search_bigram_fts SET body_text = urmail_bigrams(coalesce(NEW.body_text,''))
  WHERE rowid = NEW.entry_id;
END;

CREATE TRIGGER archive_search_bigram_fts_content_ad
AFTER DELETE ON archive_content BEGIN
  DELETE FROM archive_search_bigram_fts WHERE rowid = OLD.entry_id;
END;
