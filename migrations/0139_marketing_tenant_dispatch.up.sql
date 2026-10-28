-- Derived, platform-only round-robin state shared by campaigns and flows.
CREATE SEQUENCE platform_marketing_dispatch_turn_seq AS bigint;
ALTER SEQUENCE platform_marketing_dispatch_turn_seq OWNER TO urmail_platform;
CREATE TABLE platform_marketing_dispatch (
 tenant_id bigint PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
 last_turn bigint NOT NULL DEFAULT 0 CHECK (last_turn>=0)
);
ALTER TABLE platform_marketing_dispatch OWNER TO urmail_platform;
ALTER TABLE platform_marketing_dispatch ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform_marketing_dispatch FORCE ROW LEVEL SECURITY;
CREATE POLICY marketing_dispatch_platform ON platform_marketing_dispatch TO urmail_platform USING (true) WITH CHECK (true);
REVOKE ALL ON platform_marketing_dispatch FROM PUBLIC,urmail_app;
REVOKE ALL ON SEQUENCE platform_marketing_dispatch_turn_seq FROM PUBLIC,urmail_app;
INSERT INTO platform_marketing_dispatch(tenant_id) SELECT id FROM tenants;
CREATE FUNCTION urmail_seed_marketing_dispatch() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 INSERT INTO public.platform_marketing_dispatch(tenant_id) VALUES(NEW.id) ON CONFLICT DO NOTHING;
 RETURN NEW;
END;
$$;
ALTER FUNCTION urmail_seed_marketing_dispatch() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_seed_marketing_dispatch() FROM PUBLIC,urmail_app;
CREATE TRIGGER tenant_marketing_dispatch AFTER INSERT ON tenants
FOR EACH ROW EXECUTE FUNCTION urmail_seed_marketing_dispatch();
