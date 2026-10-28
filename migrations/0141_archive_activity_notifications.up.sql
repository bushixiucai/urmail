-- Persist archive activity in the existing in-app outbox in the same transaction.
-- No source text, subject, evidence URL, approval reason, or recipient email is copied.
CREATE FUNCTION urmail_archive_activity_notice() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE previous_tenant text:=current_setting('urmail.tenant_id',true); recipient_id bigint; payload jsonb; event text;
BEGIN
 IF TG_TABLE_NAME='archive_approval_requests' THEN
  IF TG_OP='UPDATE' AND NEW.state=OLD.state THEN RETURN NEW; END IF;
  PERFORM set_config('urmail.tenant_id',NEW.tenant_id::text,true);
  payload:=jsonb_build_object('case_id',NEW.investigation_id,'approval_id',NEW.id,'state',NEW.state);
  FOR recipient_id IN
   SELECT m.user_id FROM archive_investigation_members m
   WHERE m.tenant_id=NEW.tenant_id AND m.investigation_id=NEW.investigation_id AND m.removed_at IS NULL
   AND urmail_archive_case_grant(NEW.tenant_id,m.user_id,'full_content')
   AND (NEW.state<>'pending' OR urmail_archive_case_member(NEW.tenant_id,NEW.investigation_id,m.user_id))
   AND urmail_archive_case_grant(NEW.tenant_id,m.user_id,'export')
   AND ((NEW.state='pending' AND m.user_id<>NEW.requester_user_id) OR (NEW.state<>'pending' AND m.user_id=NEW.requester_user_id))
  LOOP
   INSERT INTO notification_outbox(tenant_id,user_id,kind,message_key,params,dedupe_key,channel,state,delivered_at)
   VALUES(NEW.tenant_id,recipient_id,'archive_approval','notification.archive_approval_'||NEW.state,payload,
    'archive-approval:'||NEW.id||':'||NEW.state||':'||recipient_id,'in_app','delivered',statement_timestamp())
   ON CONFLICT(dedupe_key) DO NOTHING;
  END LOOP;
 ELSE
  IF NEW.status='preview' OR (TG_OP='UPDATE' AND NEW.status=OLD.status) THEN RETURN NEW; END IF;
  IF EXISTS(SELECT 1 FROM user_tenant_roles r WHERE r.tenant_id=NEW.tenant_id AND r.user_id=NEW.requested_by AND r.role IN('owner','admin')) THEN
   event:=CASE WHEN NEW.status='pending' THEN 'requested' ELSE 'completed' END;
   payload:=jsonb_build_object('recall_id',NEW.id,'status',NEW.status,'scope','local_mailbox');
   INSERT INTO notification_outbox(tenant_id,user_id,kind,message_key,params,dedupe_key,channel,state,delivered_at)
   VALUES(NEW.tenant_id,NEW.requested_by,'archive_recall','notification.archive_recall_'||event,payload,
    'archive-recall:'||NEW.id||':'||coalesce(to_jsonb(NEW)->>'attempt_epoch','0')||':'||NEW.status||':'||NEW.requested_by,'in_app','delivered',statement_timestamp())
   ON CONFLICT(dedupe_key) DO NOTHING;
  END IF;
 END IF;
 PERFORM set_config('urmail.tenant_id',coalesce(previous_tenant,''),true);
 RETURN NEW;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('urmail.tenant_id',coalesce(previous_tenant,''),true);
 RAISE;
END $$;
ALTER FUNCTION urmail_archive_activity_notice() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_archive_activity_notice() FROM PUBLIC,urmail_app,urmail_dovecot;
CREATE TRIGGER archive_approval_activity AFTER INSERT OR UPDATE ON archive_approval_requests
FOR EACH ROW EXECUTE FUNCTION urmail_archive_activity_notice();
CREATE TRIGGER archive_recall_activity AFTER INSERT OR UPDATE ON recall_requests
FOR EACH ROW EXECUTE FUNCTION urmail_archive_activity_notice();

-- Notifications retain only minimal IDs, but even these are hidden immediately
-- when the recipient loses membership or the relevant live administrative grant.
CREATE FUNCTION urmail_archive_notification_visible(t bigint,u bigint,k text,p jsonb) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT CASE WHEN k NOT IN('archive_approval','archive_recall') THEN true ELSE
 t=nullif(current_setting('urmail.tenant_id',true),'')::bigint
 AND u=nullif(current_setting('urmail.user_id',true),'')::bigint
 AND CASE k
 WHEN 'archive_approval' THEN EXISTS(SELECT 1 FROM archive_investigations i
 WHERE i.tenant_id=t AND p @> jsonb_build_object('case_id',i.id)
 AND EXISTS(SELECT 1 FROM archive_investigation_members m WHERE m.tenant_id=t AND m.investigation_id=i.id AND m.user_id=u AND m.removed_at IS NULL)
 AND urmail_archive_case_grant(t,u,'full_content') AND urmail_archive_case_grant(t,u,'export'))
 WHEN 'archive_recall' THEN EXISTS(SELECT 1 FROM recall_requests r JOIN user_tenant_roles a ON a.tenant_id=r.tenant_id AND a.user_id=r.requested_by
 WHERE r.tenant_id=t AND r.requested_by=u AND a.role IN('owner','admin') AND p @> jsonb_build_object('recall_id',r.id))
 ELSE false END END;
$$;
ALTER FUNCTION urmail_archive_notification_visible(bigint,bigint,text,jsonb) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_archive_notification_visible(bigint,bigint,text,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_archive_notification_visible(bigint,bigint,text,jsonb) TO urmail_app,urmail_platform;

-- A service may expire a product approval only after its actual deadline.
-- Keep the existing restore invalidation authority and all independent-actor guards.
DO $$ DECLARE definition text; patched text; BEGIN
 SELECT pg_get_functiondef('urmail_archive_approval_bound()'::regprocedure) INTO definition;
 patched:=replace(definition,
  'AND NEW.decision_reason LIKE ''RESTORE_RECONCILIATION_REQUIRED:%'' THEN RETURN NEW;',
  'AND (NEW.decision_reason LIKE ''RESTORE_RECONCILIATION_REQUIRED:%'' OR (NEW.expires_at<=statement_timestamp() AND NEW.decision_reason=''APPROVAL_EXPIRED'')) THEN RETURN NEW;');
 IF patched=definition THEN RAISE EXCEPTION 'ARCHIVE_EXPIRY_GUARD_PATCH_REQUIRED'; END IF;
 EXECUTE patched;
END $$;
