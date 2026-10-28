-- Never discard immutable definitions or waiting instances during rollback.
DO $$ BEGIN IF EXISTS(SELECT 1 FROM marketing_flow_versions) THEN
 RAISE EXCEPTION 'FLOW_ROLLBACK_REQUIRES_REVIEWED_DRAIN'; END IF; END $$;
DROP TRIGGER stop_revoked_flow_contacts ON marketing_contacts;
DROP FUNCTION urmail_stop_revoked_flow_contacts();
DROP TRIGGER stop_suppressed_marketing_flows ON marketing_suppressions;
DROP FUNCTION urmail_stop_suppressed_marketing_flows();
DROP FUNCTION urmail_record_marketing_flow_audit(bigint,bigint,text,jsonb);
DROP TRIGGER guard_marketing_version ON marketing_flow_versions;
DROP TRIGGER guard_marketing_content ON marketing_flow_content_snapshots;
DROP FUNCTION urmail_guard_marketing_snapshot();
DROP FUNCTION urmail_retry_marketing_flow_instance(bigint,bigint,bigint,boolean);
DROP FUNCTION urmail_release_marketing_flow_reservation(bigint,bigint,text,text,bigint);
DROP FUNCTION urmail_begin_marketing_flow_send(bigint,bigint,text,text,bigint,bigint);
DROP FUNCTION urmail_commit_marketing_flow_fenced(bigint,bigint,text,text,bigint,text,boolean,boolean);
DROP TRIGGER guard_published_marketing_step ON marketing_flow_steps;
DROP FUNCTION urmail_guard_published_marketing_step();
DROP TABLE marketing_flow_node_executions;
ALTER TABLE marketing_flow_positions DROP CONSTRAINT marketing_flow_positions_tenant_id_entry_id_fkey;
ALTER TABLE marketing_flow_positions DROP CONSTRAINT marketing_flow_positions_tenant_id_flow_window_id_fkey;
ALTER TABLE marketing_flow_positions DROP CONSTRAINT marketing_flow_positions_tenant_id_id_key;
ALTER TABLE marketing_flow_positions DROP COLUMN entry_id,DROP COLUMN event_facts,DROP COLUMN last_error,DROP COLUMN next_attempt_at,DROP COLUMN attempt_count,DROP COLUMN stopped_at,DROP COLUMN flow_window_id,DROP COLUMN flow_reserved;
ALTER TABLE marketing_flow_steps DROP CONSTRAINT marketing_flow_steps_kind_check;
ALTER TABLE marketing_flow_steps ADD CONSTRAINT marketing_flow_steps_kind_check CHECK(kind IN('send','wait','stop'));
ALTER TABLE marketing_flow_steps DROP COLUMN condition_field,DROP COLUMN condition_operator,DROP COLUMN condition_value;
DROP TABLE marketing_flow_entries,marketing_flow_content_snapshots,marketing_flow_versions,marketing_flow_definitions;
