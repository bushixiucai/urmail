DO $$ BEGIN IF EXISTS(SELECT 1 FROM recall_requests WHERE status='local_recalled') THEN RAISE EXCEPTION 'LOCAL_RECALL_DOWN_PREFLIGHT: completed local recall history cannot be represented by the previous schema'; END IF; END $$;
CREATE OR REPLACE FUNCTION urmail_recall_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='INSERT' AND NEW.status<>'pending' THEN RAISE EXCEPTION 'recall must start pending' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' AND (OLD.status<>'pending' OR NEW.status NOT IN('pending','failed','expired')) THEN RAISE EXCEPTION 'remote delivery recall is not confirmed' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
ALTER TABLE recall_requests DROP CONSTRAINT recall_requests_status_check;
ALTER TABLE recall_requests ADD CONSTRAINT recall_requests_status_check CHECK(status IN('pending','delivered','failed','expired'));
ALTER TABLE recall_requests DROP CONSTRAINT recall_archive_tenant_fk,DROP CONSTRAINT recall_target_unique,DROP CONSTRAINT recall_mailbox_tenant_fk,DROP COLUMN archive_entry_id,DROP COLUMN mailbox_id,DROP COLUMN completed_at;

ALTER TABLE archive_entries DROP CONSTRAINT archive_id_tenant_unique;
