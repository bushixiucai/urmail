ALTER TABLE archive_entries ADD CONSTRAINT archive_id_tenant_unique UNIQUE(id,tenant_id);
ALTER TABLE recall_requests ADD COLUMN archive_entry_id bigint, ADD COLUMN mailbox_id bigint, ADD COLUMN completed_at timestamptz;
ALTER TABLE recall_requests ADD CONSTRAINT recall_archive_tenant_fk FOREIGN KEY(archive_entry_id,tenant_id) REFERENCES archive_entries(id,tenant_id) ON DELETE RESTRICT;
ALTER TABLE recall_requests ADD CONSTRAINT recall_mailbox_tenant_fk FOREIGN KEY(tenant_id,mailbox_id) REFERENCES mailboxes(tenant_id,id) ON DELETE RESTRICT;
ALTER TABLE recall_requests ADD CONSTRAINT recall_target_unique UNIQUE(tenant_id,archive_entry_id);
ALTER TABLE recall_requests DROP CONSTRAINT recall_requests_status_check;
ALTER TABLE recall_requests ADD CONSTRAINT recall_requests_status_check CHECK(status IN('pending','delivered','failed','expired','local_recalled'));
CREATE OR REPLACE FUNCTION urmail_recall_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='INSERT' AND NEW.status<>'pending' THEN RAISE EXCEPTION 'recall must start pending' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' AND (OLD.status<>'pending' OR NEW.status NOT IN('pending','failed','expired','local_recalled')) THEN RAISE EXCEPTION 'remote delivery recall is not confirmed' USING ERRCODE='23514'; END IF;
 IF NEW.status='local_recalled' AND current_user<>'urmail_platform' THEN RAISE EXCEPTION 'only the verified local agent can complete a recall' USING ERRCODE='42501'; END IF;
 IF NEW.status='local_recalled' AND (NEW.mailbox_id IS NULL OR NEW.completed_at IS NULL) THEN RAISE EXCEPTION 'local recall requires a mailbox and completion time' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
