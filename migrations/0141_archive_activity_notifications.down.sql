DROP TRIGGER archive_recall_activity ON recall_requests;
DROP TRIGGER archive_approval_activity ON archive_approval_requests;
DROP FUNCTION urmail_archive_activity_notice();
DROP FUNCTION urmail_archive_notification_visible(bigint,bigint,text,jsonb);
-- Delivered notices are historical facts; downgrade does not erase them.

DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_archive_approval_bound()'::regprocedure) INTO definition;
 EXECUTE replace(definition,
  'AND (NEW.decision_reason LIKE ''RESTORE_RECONCILIATION_REQUIRED:%'' OR (NEW.expires_at<=statement_timestamp() AND NEW.decision_reason=''APPROVAL_EXPIRED'')) THEN RETURN NEW;',
  'AND NEW.decision_reason LIKE ''RESTORE_RECONCILIATION_REQUIRED:%'' THEN RETURN NEW;');
END $$;
