-- name: ReserveDNSZoneVersion :one
INSERT INTO dns_zone_versions(domain,content_hash,serial) VALUES($1,$2,$3)
ON CONFLICT(domain) DO UPDATE SET
 serial=CASE WHEN dns_zone_versions.content_hash=EXCLUDED.content_hash THEN dns_zone_versions.serial ELSE (dns_zone_versions.serial+1)%4294967296 END,
 content_hash=EXCLUDED.content_hash,updated_at=now()
RETURNING serial;
