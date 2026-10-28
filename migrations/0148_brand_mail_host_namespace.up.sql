-- Existing collisions require an explicit operator decision; never steal a
-- host or silently invalidate already configured mail/brand identities.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM brand_hosts h JOIN domains d ON lower(rtrim(d.fqdn,'.'))=lower(rtrim(h.hostname,'.'))) THEN
  RAISE EXCEPTION 'existing brand/mail hostname collision; resolve bindings before migration 0148' USING ERRCODE='23505';
 END IF;
END $$;

-- One hostname cannot simultaneously be a platform brand entry and a tenant
-- mail domain. Serialize both insertion directions before checking ownership.
CREATE FUNCTION urmail_guard_host_namespace() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE candidate text;
BEGIN
 IF TG_TABLE_NAME='domains' THEN candidate:=lower(rtrim(NEW.fqdn,'.')); ELSE candidate:=lower(rtrim(NEW.hostname,'.')); END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended(candidate,148));
 IF TG_TABLE_NAME='domains' THEN
  IF EXISTS(SELECT 1 FROM public.brand_hosts WHERE lower(rtrim(hostname,'.'))=candidate) THEN
   RAISE EXCEPTION 'hostname is reserved by a brand host' USING ERRCODE='23505';
  END IF;
 ELSE
  IF EXISTS(SELECT 1 FROM public.domains WHERE lower(rtrim(fqdn,'.'))=candidate) THEN
   RAISE EXCEPTION 'hostname is reserved by a mail domain' USING ERRCODE='23505';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
ALTER FUNCTION urmail_guard_host_namespace() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_guard_host_namespace() FROM PUBLIC,urmail_app,urmail_dovecot;
CREATE TRIGGER domains_host_namespace_guard BEFORE INSERT OR UPDATE OF fqdn ON domains
FOR EACH ROW EXECUTE FUNCTION urmail_guard_host_namespace();
CREATE TRIGGER brand_hosts_namespace_guard BEFORE INSERT OR UPDATE OF hostname ON brand_hosts
FOR EACH ROW EXECUTE FUNCTION urmail_guard_host_namespace();
