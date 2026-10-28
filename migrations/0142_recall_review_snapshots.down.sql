DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM recall_requests WHERE status='uncertain' OR (preview_token IS NOT NULL AND status IN('preview','pending'))) THEN
  RAISE EXCEPTION 'RECALL_DOWN_REQUIRES_RECONCILIATION: active reviewed actions must be resolved before downgrade'; END IF;
END $$;
-- Retain completed review facts in the existing immutable audit history.
INSERT INTO audit_logs(tenant_id,actor_user_id,action,target_type,target_id,detail)
SELECT tenant_id,requested_by,'compliance.recall.review_downgrade','recall',id::text,
 jsonb_build_object('epoch',attempt_epoch,'digest',preview_digest,'snapshot',convert_from(preview_snapshot,'UTF8')::jsonb,'selection',selected_copy_keys,'result',local_result,'history',result_history)
FROM recall_requests WHERE preview_token IS NOT NULL;
CREATE OR REPLACE FUNCTION urmail_recall_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='INSERT' AND NEW.status<>'pending' THEN RAISE EXCEPTION 'recall must start pending' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' AND (OLD.status<>'pending' OR NEW.status NOT IN('pending','failed','expired','local_recalled')) THEN RAISE EXCEPTION 'remote delivery recall is not confirmed' USING ERRCODE='23514'; END IF;
 IF NEW.status='local_recalled' AND current_user<>'urmail_platform' THEN RAISE EXCEPTION 'only the verified local agent can complete a recall' USING ERRCODE='42501'; END IF;
 IF NEW.status='local_recalled' AND (NEW.mailbox_id IS NULL OR NEW.completed_at IS NULL) THEN RAISE EXCEPTION 'local recall requires a mailbox and completion time' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_archive_activity_notice()'::regprocedure) INTO definition;
 EXECUTE replace(definition,'coalesce((to_jsonb(NEW)->>''preview_actor_id'')::bigint,NEW.requested_by)','NEW.requested_by');
 SELECT pg_get_functiondef('urmail_archive_notification_visible(bigint,bigint,text,jsonb)'::regprocedure) INTO definition;
 EXECUTE replace(definition,'coalesce(r.preview_actor_id,r.requested_by)','r.requested_by');
END $$;
ALTER TABLE recall_requests DROP COLUMN preview_actor_id,DROP COLUMN preview_token,DROP COLUMN preview_digest,DROP COLUMN preview_snapshot,DROP COLUMN preview_expires_at,DROP COLUMN confirmed_at,DROP COLUMN selected_copy_keys,DROP COLUMN attempt_epoch,DROP COLUMN local_result,DROP COLUMN result_history;
ALTER TABLE recall_requests DROP CONSTRAINT recall_requests_status_check;
ALTER TABLE recall_requests ADD CONSTRAINT recall_requests_status_check CHECK(status IN('pending','delivered','failed','expired','local_recalled'));
