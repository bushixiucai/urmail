-- Existing plaintext may be migrated in controlled batches; new writes must encrypt.
CREATE FUNCTION urmail_require_encrypted_totp() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.mfa_secret IS NOT NULL AND NEW.mfa_secret NOT LIKE 'enc:v1:%' AND (TG_OP='INSERT' OR NEW.mfa_secret IS DISTINCT FROM OLD.mfa_secret) THEN
  RAISE EXCEPTION 'TOTP_ENCRYPTION_REQUIRED' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_require_encrypted_totp() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_require_encrypted_totp() FROM PUBLIC;
CREATE TRIGGER totp_encrypted_writes BEFORE INSERT OR UPDATE OF mfa_secret ON users FOR EACH ROW EXECUTE FUNCTION urmail_require_encrypted_totp();
