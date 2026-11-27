-- name: PlatformEvidenceVisible :one
-- scope: platform

SELECT EXISTS(
  SELECT 1 FROM archive_approval_requests r
  JOIN archive_investigation_members m ON m.tenant_id=r.tenant_id AND m.investigation_id=r.investigation_id
  WHERE r.tenant_id=?1 AND r.id=?2 AND r.state='approved' AND r.expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000)
   AND r.evidence_digest=?4 AND m.user_id=?3 AND m.removed_at IS NULL) AS visible;

-- PlatformReadEvidenceBody 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- urmail_read_evidence_body = 可见性谓词 + sha256 摘要校验。
