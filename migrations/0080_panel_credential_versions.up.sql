-- Panel credentials and sessions share a durable revocation generation.
ALTER TABLE users ADD COLUMN credential_version bigint NOT NULL DEFAULT 0 CHECK(credential_version>=0);
ALTER TABLE sessions ADD COLUMN credential_version bigint NOT NULL DEFAULT 0 CHECK(credential_version>=0);
UPDATE sessions s SET credential_version=u.credential_version FROM users u WHERE u.id=s.user_id;
ALTER TABLE application_credentials ADD COLUMN credential_version bigint NOT NULL DEFAULT 0;
UPDATE application_credentials a SET credential_version=u.credential_version FROM users u WHERE u.id=a.user_id;

CREATE FUNCTION urmail_panel_credential_version() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF (NEW.password_hash IS DISTINCT FROM OLD.password_hash AND current_setting('urmail.password_rehash',true) IS DISTINCT FROM 'true')
    OR NEW.mfa_enabled IS DISTINCT FROM OLD.mfa_enabled
    OR NEW.mfa_secret IS DISTINCT FROM OLD.mfa_secret THEN
  NEW.credential_version:=OLD.credential_version+1;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_panel_credential_version() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_panel_credential_version() FROM PUBLIC;
CREATE TRIGGER panel_credential_version BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION urmail_panel_credential_version();

CREATE FUNCTION urmail_rotate_panel_password(p_user bigint,p_expected text,p_expected_version bigint,p_hash text,p_keep text) RETURNS bigint LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE generation bigint;
BEGIN
 PERFORM 1 FROM users WHERE id=p_user AND password_hash=p_expected AND credential_version=p_expected_version AND status='active' FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'CREDENTIAL_CHANGED' USING ERRCODE='40001'; END IF;
 PERFORM 1 FROM sessions WHERE id=p_keep AND user_id=p_user AND kind='panel' AND credential_version=p_expected_version AND expires_at>now() FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'SESSION_REVOKED' USING ERRCODE='42501'; END IF;
 UPDATE users SET password_hash=p_hash,password_changed_at=now() WHERE id=p_user RETURNING credential_version INTO generation;
 DELETE FROM sessions WHERE user_id=p_user AND id<>p_keep;
 UPDATE sessions SET credential_version=generation WHERE user_id=p_user AND id=p_keep;
 INSERT INTO audit_logs(actor_user_id,action,target_type,target_id,detail) VALUES(p_user,'auth.credential.rotate','users',p_user::text,jsonb_build_object('credential_version',generation));
 RETURN generation;
END $$;
ALTER FUNCTION urmail_rotate_panel_password(bigint,text,bigint,text,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_rotate_panel_password(bigint,text,bigint,text,text) FROM PUBLIC;

CREATE FUNCTION urmail_change_panel_mfa(p_user bigint,p_expected bigint,p_secret text,p_enabled boolean,p_codes jsonb,p_keep text) RETURNS bigint LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE generation bigint;
BEGIN
 PERFORM 1 FROM users WHERE id=p_user AND credential_version=p_expected AND status='active' FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'CREDENTIAL_CHANGED' USING ERRCODE='40001'; END IF;
 PERFORM 1 FROM sessions WHERE id=p_keep AND user_id=p_user AND kind='panel' AND credential_version=p_expected AND expires_at>now() FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'SESSION_REVOKED' USING ERRCODE='42501'; END IF;
 UPDATE users SET mfa_secret=p_secret,mfa_enabled=p_enabled,mfa_recovery_codes=p_codes WHERE id=p_user RETURNING credential_version INTO generation;
 DELETE FROM sessions WHERE user_id=p_user AND id<>p_keep;
 UPDATE sessions SET credential_version=generation,mfa_verified_at=CASE WHEN p_enabled THEN now() ELSE NULL END WHERE user_id=p_user AND id=p_keep;
 INSERT INTO audit_logs(actor_user_id,action,target_type,target_id,detail) VALUES(p_user,'auth.credential.rotate','users',p_user::text,jsonb_build_object('credential_version',generation));
 RETURN generation;
END $$;
ALTER FUNCTION urmail_change_panel_mfa(bigint,bigint,text,boolean,jsonb,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_change_panel_mfa(bigint,bigint,text,boolean,jsonb,text) FROM PUBLIC;
