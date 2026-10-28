-- name: RecallMessageCopies :many
SELECT a.id,a.mailbox_id,a.subject,a.message_id,a.legal_hold,a.deleted_at,m.address,m.status AS mailbox_status,
r.id AS recall_id,r.status AS recall_status,r.requested_by
FROM archive_entries a LEFT JOIN mailboxes m ON m.tenant_id=a.tenant_id AND m.id=a.mailbox_id
LEFT JOIN recall_requests r ON r.tenant_id=a.tenant_id AND r.archive_entry_id=a.id
WHERE a.tenant_id=$1 AND a.message_id=(SELECT source.message_id FROM archive_entries source WHERE source.tenant_id=$1 AND source.id=$2)
ORDER BY a.id LIMIT 101;

-- name: StoreRecallReviewPreview :one
WITH changed AS (
 INSERT INTO recall_requests(tenant_id,archive_entry_id,mailbox_id,message_id,requested_by,status,
 preview_actor_id,preview_token,preview_digest,preview_snapshot,preview_expires_at,attempt_epoch)
 SELECT a.tenant_id,a.id,a.mailbox_id,a.message_id, @actor_id::bigint,'preview', @actor_id::bigint, @token::text, @digest::text, @snapshot::bytea,
 statement_timestamp()+interval '5 minutes',1 FROM archive_entries a WHERE a.tenant_id= @tenant_id AND a.id= @entry_id
 ON CONFLICT(tenant_id,archive_entry_id) DO UPDATE SET status='preview',reason=NULL,completed_at=NULL,
 preview_actor_id=EXCLUDED.preview_actor_id,preview_token=EXCLUDED.preview_token,preview_digest=EXCLUDED.preview_digest,preview_snapshot=EXCLUDED.preview_snapshot,
 preview_expires_at=EXCLUDED.preview_expires_at,confirmed_at=NULL,selected_copy_keys='[]',attempt_epoch=recall_requests.attempt_epoch+1,
 result_history=CASE WHEN recall_requests.confirmed_at IS NOT NULL THEN recall_requests.result_history||jsonb_build_array(jsonb_build_object('actor_id',recall_requests.preview_actor_id,'epoch',recall_requests.attempt_epoch,'digest',recall_requests.preview_digest,'selection',recall_requests.selected_copy_keys,'status',recall_requests.status,'result',recall_requests.local_result,'completed_at',recall_requests.completed_at)) ELSE recall_requests.result_history END,
 local_result='{"items":[]}'::jsonb
 WHERE (recall_requests.status<>'pending' OR recall_requests.preview_token IS NULL)
 RETURNING recall_requests.*
)
SELECT * FROM changed UNION ALL SELECT * FROM recall_requests r WHERE r.tenant_id= @tenant_id AND r.archive_entry_id= @entry_id AND NOT EXISTS(SELECT 1 FROM changed) LIMIT 1;

-- name: ConfirmRecallReview :one
WITH changed AS (
 UPDATE recall_requests r SET status='pending',confirmed_at=statement_timestamp(),selected_copy_keys= @selected::jsonb
 WHERE r.tenant_id= @tenant_id AND r.id= @recall_id AND r.preview_actor_id= @actor_id AND r.preview_token= @token AND r.preview_digest= @digest
 AND r.preview_expires_at>statement_timestamp() AND r.status='preview'
 RETURNING r.*
)
SELECT * FROM changed UNION ALL SELECT * FROM recall_requests r
WHERE r.tenant_id= @tenant_id AND r.id= @recall_id AND r.preview_actor_id= @actor_id AND r.preview_token= @token AND r.preview_digest= @digest
AND r.status IN('pending','local_recalled','failed','uncertain','expired') AND r.selected_copy_keys= @selected::jsonb
AND NOT EXISTS(SELECT 1 FROM changed) LIMIT 1;
