-- A non-secret instance epoch invalidates DAV/EAS tokens after a rewind.
-- Only restore-quarantine tooling writes the underlying platform setting.
CREATE FUNCTION urmail_restore_sync_epoch() RETURNS bigint
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT COALESCE((SELECT (value->>'sync_epoch')::bigint FROM public.settings
  WHERE scope='platform' AND scope_id=0 AND key='restore_replay_review'),1)::bigint;
$$;
ALTER FUNCTION urmail_restore_sync_epoch() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_restore_sync_epoch() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_restore_sync_epoch() TO urmail_app,urmail_platform;
