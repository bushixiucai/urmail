-- name: PlatformEvidenceVisible :one
-- scope: platform

SELECT urmail_evidence_manifest_visible(?1,?2,?3,?4) AS visible;

-- name: PlatformReadEvidenceBody :one
-- scope: platform

SELECT urmail_read_evidence_body(?1,?2,?3,?4);
