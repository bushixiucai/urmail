-- name: PlatformCreateRegistrationRequest :exec
INSERT INTO registration_requests(token_hash,email,password_hash,tenant_name,brand_id,expires_at)
VALUES($1,$2,$3,$4,$5,$6);

-- name: PlatformFindRegistrationRequest :one
SELECT * FROM registration_requests WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE;

-- name: PlatformConsumeRegistrationRequest :exec
UPDATE registration_requests SET consumed_at=now() WHERE token_hash=$1;

-- name: PlatformRegistrationEmailExists :one
SELECT EXISTS(SELECT 1 FROM users WHERE email=$1);

-- name: PlatformCreateRegisteredUser :one
INSERT INTO users(email,password_hash) VALUES($1,$2) RETURNING id;

-- name: PlatformCreateRegisteredTenant :one
INSERT INTO tenants(brand_id,name,slug,status,owner_user_id) VALUES($1,$2,$3,'pending',$4) RETURNING id;

-- name: PlatformBindRegisteredOwner :exec
INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner');

-- name: PlatformSaveVerifiedContact :exec
INSERT INTO tenant_contacts(tenant_id,email,verified_at) VALUES($1,$2,now());

-- name: PlatformRemoveRegistrationGift :exec
UPDATE subscriptions SET status='canceled' WHERE tenant_id=$1 AND plan_id='free' AND status='active';

-- name: PlatformEnqueueContactVerification :exec
INSERT INTO notification_outbox(kind,message_key,params,dedupe_key,channel,state,recipient,recipient_verified,secret_payload)
VALUES('contact_verification','notification.contact_verification','{}',$1,'email','pending',$2,false,$3);

-- name: PlatformLockRegistrationEmail :exec
SELECT pg_advisory_xact_lock(4007,hashtext($1::text));

-- name: PlatformRegistrationTokenEmail :one
SELECT email FROM registration_requests WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now();

-- name: PlatformRevokeRegistrationRequests :exec
WITH revoked AS (
 UPDATE registration_requests SET consumed_at=now() WHERE email=$1 AND consumed_at IS NULL RETURNING token_hash
)
UPDATE notification_outbox SET state='failed',secret_payload=NULL WHERE dedupe_key IN(SELECT 'contact-verify:'||token_hash FROM revoked) AND state='pending';
