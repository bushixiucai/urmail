-- Each report is one bounded SQL snapshot. The time window selects the
-- recipient/enrollment cohort; status, suppression and outcomes are current.
-- name: SetMarketingReportBudget :exec
SELECT set_config('statement_timeout','5000ms',true);

-- name: MarketingCampaignReportSnapshot :one
WITH target AS MATERIALIZED (
 SELECT c.id,c.name FROM marketing_campaigns c WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.id=sqlc.arg(resource_id)
 AND EXISTS(SELECT 1 FROM user_tenant_roles role JOIN users actor ON actor.id=role.user_id WHERE role.tenant_id=c.tenant_id AND role.user_id=NULLIF(current_setting('urmail.user_id',true),'')::bigint AND role.role IN('owner','admin') AND actor.status='active')
), recipients AS MATERIALIZED (
 SELECT r.* FROM marketing_recipients r JOIN target c ON c.id=r.campaign_id
 WHERE r.tenant_id=sqlc.arg(tenant_id) AND r.created_at>=sqlc.arg(from_time)::timestamptz AND r.created_at<sqlc.arg(until_time)::timestamptz
), facts AS (
 SELECT 'recipient'::text AS record_kind,r.id::text AS record_id,0::integer AS version,r.id AS instance_id,r.email,
 r.status AS state,coalesce(r.result_code,'')::text AS result_code,r.created_at AS occurred_at,1::bigint AS weight,
 EXISTS(SELECT 1 FROM marketing_suppressions s WHERE s.tenant_id=sqlc.arg(tenant_id) AND s.email=r.email) AS suppressed,r.status IN('uncertain','submitting') AS uncertain
 FROM recipients r
 UNION ALL
 SELECT 'feedback',f.channel_id::text||':'||f.event_id,0,r.id,r.email,f.status,'',f.created_at,1,false,false
 FROM marketing_feedback f JOIN recipients r ON r.tenant_id=f.tenant_id AND r.id=f.recipient_id
 WHERE f.tenant_id=sqlc.arg(tenant_id)
), bounded AS MATERIALIZED (SELECT * FROM facts ORDER BY record_kind,record_id LIMIT 10001)
SELECT jsonb_build_object('name',target.name,'rows',coalesce((SELECT jsonb_agg(to_jsonb(b) ORDER BY b.record_kind,b.record_id) FROM bounded b),'[]'::jsonb)) AS payload FROM target;

-- name: MarketingFlowReportSnapshot :one
WITH target AS MATERIALIZED (
 SELECT d.id,d.name FROM marketing_flow_definitions d WHERE d.tenant_id=sqlc.arg(tenant_id) AND d.id=sqlc.arg(resource_id)
 AND EXISTS(SELECT 1 FROM user_tenant_roles role JOIN users actor ON actor.id=role.user_id WHERE role.tenant_id=d.tenant_id AND role.user_id=NULLIF(current_setting('urmail.user_id',true),'')::bigint AND role.role IN('owner','admin') AND actor.status='active')
), entries AS MATERIALIZED (
 SELECT e.*,v.version FROM marketing_flow_entries e JOIN target d ON d.id=e.definition_id
 JOIN marketing_flow_versions v ON v.tenant_id=e.tenant_id AND v.id=e.version_id AND v.definition_id=e.definition_id
 WHERE e.tenant_id=sqlc.arg(tenant_id) AND e.created_at>=sqlc.arg(from_time)::timestamptz AND e.created_at<sqlc.arg(until_time)::timestamptz
), positions AS MATERIALIZED (
 SELECT p.*,e.version,e.created_at AS entered_at,f.status AS flow_status,s.kind AS step_kind
 FROM marketing_flow_positions p JOIN entries e ON e.tenant_id=p.tenant_id AND e.id=p.entry_id
 JOIN marketing_flows f ON f.tenant_id=p.tenant_id AND f.id=p.flow_id
 JOIN marketing_flow_steps s ON s.tenant_id=p.tenant_id AND s.flow_id=p.flow_id AND s.step_key=p.step_key
 WHERE p.tenant_id=sqlc.arg(tenant_id)
), facts AS (
 SELECT 'entry'::text AS record_kind,e.id::text AS record_id,e.version,0::bigint AS instance_id,''::text AS email,
 e.source AS state,e.event_key AS result_code,e.created_at AS occurred_at,e.recipient_count::bigint AS weight,false AS suppressed,false AS uncertain
 FROM entries e
 UNION ALL
 SELECT 'instance',p.id::text,p.version,p.id,p.email,
 CASE WHEN p.stopped_at IS NOT NULL THEN 'stopped' WHEN p.unsubscribed THEN 'unsubscribed' WHEN p.delivery_uncertain THEN 'uncertain'
 WHEN p.step_kind='stop' THEN 'completed' WHEN p.flow_status<>'active' THEN 'paused' WHEN p.last_error<>'' THEN 'failed'
 WHEN p.wait_until>statement_timestamp() THEN 'waiting' ELSE 'pending' END,
 p.last_error,p.entered_at,1,EXISTS(SELECT 1 FROM marketing_suppressions s WHERE s.tenant_id=sqlc.arg(tenant_id) AND s.email=p.email),p.delivery_uncertain
 FROM positions p
 UNION ALL
 SELECT 'execution',x.id::text,p.version,p.id,p.email,x.status,x.error_code,x.created_at,1,false,x.status='uncertain'
 FROM marketing_flow_node_executions x JOIN positions p ON p.tenant_id=x.tenant_id AND p.id=x.position_id
 WHERE x.tenant_id=sqlc.arg(tenant_id)
), bounded AS MATERIALIZED (SELECT * FROM facts ORDER BY record_kind,record_id LIMIT 10001)
SELECT jsonb_build_object('name',target.name,'rows',coalesce((SELECT jsonb_agg(to_jsonb(b) ORDER BY b.record_kind,b.record_id) FROM bounded b),'[]'::jsonb)) AS payload FROM target;
