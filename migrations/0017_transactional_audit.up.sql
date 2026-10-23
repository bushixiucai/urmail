CREATE FUNCTION urmail_record_audit(p_tenant bigint,p_user bigint,p_ip inet,p_action text,p_target text) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint OR p_user IS DISTINCT FROM nullif(current_setting('urmail.user_id',true),'')::bigint OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=p_tenant AND user_id=p_user) THEN RAISE EXCEPTION 'invalid audit scope' USING ERRCODE='42501'; END IF;
 IF length(p_action)>256 OR length(p_target)>2048 THEN RAISE EXCEPTION 'invalid audit metadata' USING ERRCODE='22023'; END IF;
 INSERT INTO audit_logs(tenant_id,actor_user_id,actor_ip,action,target_type,target_id) VALUES(p_tenant,p_user,p_ip,p_action,'http',p_target);
END $$;
ALTER FUNCTION urmail_record_audit(bigint,bigint,inet,text,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_record_audit(bigint,bigint,inet,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_record_audit(bigint,bigint,inet,text,text) TO urmail_app;
