-- ARC-001 / PD-112 (NEW-21): archive retrieval and export require an
-- independently authorized subject ("只有独立获权主体可检索/导出本范围").
-- The grant creator must never be the grant beneficiary: a tenant owner
-- cannot authorize themself to search or export archived content. The
-- invariant is enforced at the DATABASE layer for INSERT and for the only
-- existing re-activation path (UPDATE, which the 0035 immutability guard
-- confines to revocation).
--
-- Scope note: retention_manage is intentionally out of scope. The retention
-- flow (migration 0058) authorizes the acting administrator through explicit
-- preview/confirmation steps and requires the grant beneficiary to be the
-- acting user, so a self-held retention_manage grant is that flow's designed
-- input, not an archive retrieval authority.
CREATE FUNCTION urmail_require_grant_independence() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 IF NEW.scope IN('metadata_only','full_content','export') AND (
  (NEW.granted_by IS NOT NULL AND NEW.granted_by=NEW.user_id)
  OR (NEW.granted_by IS NULL AND NEW.user_id=nullif(current_setting('urmail.user_id',true),'')::bigint)
 ) THEN
  RAISE EXCEPTION 'COMPLIANCE_GRANT_SELF_AUTHORIZATION' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_require_grant_independence() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_require_grant_independence() FROM PUBLIC;
-- Alphabetical BEFORE-trigger order runs compliance_grants_content_owner_guard
-- first, so granted_by already carries the acting creator when this fires.
CREATE TRIGGER compliance_grants_independence_guard
 BEFORE INSERT OR UPDATE ON compliance_grants
 FOR EACH ROW EXECUTE FUNCTION urmail_require_grant_independence();
