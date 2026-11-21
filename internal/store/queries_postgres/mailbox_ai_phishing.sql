-- name: PlatformUpsertMailboxPhishingFinding :exec
INSERT INTO mailbox_ai_phishing_findings(
  tenant_id, mailbox_id, folder, uid_validity, uid, input_digest, result
) VALUES($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT(mailbox_id, folder, uid_validity, uid) DO UPDATE
SET input_digest = EXCLUDED.input_digest,
    result = EXCLUDED.result,
    updated_at = now();

-- name: PlatformGetMailboxPhishingFinding :one
SELECT result, updated_at FROM mailbox_ai_phishing_findings
WHERE tenant_id = $1 AND mailbox_id = $2 AND folder = $3
  AND uid_validity = $4 AND uid = $5;
