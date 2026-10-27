-- name: PlatformEvidenceVisible :one
SELECT urmail_evidence_manifest_visible($1,$2,$3,$4)::boolean AS visible;

-- name: PlatformReadEvidenceBody :one
SELECT urmail_read_evidence_body($1,$2,$3,$4);
