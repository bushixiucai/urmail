DROP FUNCTION urmail_change_panel_mfa(bigint,bigint,text,boolean,jsonb,text);
DROP FUNCTION urmail_rotate_panel_password(bigint,text,bigint,text,text);
DROP TRIGGER panel_credential_version ON users;
DROP FUNCTION urmail_panel_credential_version();
-- Do not resurrect sessions whose generation has been revoked.
DELETE FROM sessions s USING users u WHERE s.user_id=u.id AND s.credential_version<>u.credential_version;
UPDATE application_credentials a SET revoked_at=COALESCE(a.revoked_at,now()) FROM users u WHERE a.user_id=u.id AND a.credential_version<>u.credential_version;
ALTER TABLE application_credentials DROP COLUMN credential_version;
ALTER TABLE sessions DROP COLUMN credential_version;
ALTER TABLE users DROP COLUMN credential_version;
