-- MAIL-02: the tenant-local alias validator cannot see other tenants' rules,
-- but the whole-platform Postfix render rejects cross-tenant routing cycles.
-- This predicate lets a writer check, before persisting, whether a candidate
-- alias would close such a cycle. It returns a boolean only: no rows, keys or
-- graph structure from other tenants are exposed.
--
-- The edge set mirrors what the generator renders: non-catchall active
-- aliases whose domain is active/ready and whose tenant is active.
CREATE OR REPLACE FUNCTION urmail_alias_would_cycle(p_domain text, p_local text, p_target text)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
  WITH RECURSIVE edges(src, dst) AS (
    SELECT lower(a.local_part) || '@' || lower(d.fqdn), lower(a.target)
    FROM mailbox_aliases a
    JOIN domains d ON d.id = a.domain_id
    JOIN tenants t ON t.id = a.tenant_id
    WHERE a.kind <> 'catchall' AND a.active
      AND d.status = 'active' AND d.provisioning_state = 'ready'
      AND t.status = 'active'
  ),
  start_key(s) AS (
    SELECT lower(p_local) || '@' || (
      SELECT lower(fqdn) FROM domains WHERE fqdn = p_domain
    )
  ),
  -- The candidate adds the edge start_key -> p_target. It closes a cycle
  -- exactly when the existing graph already routes p_target back to
  -- start_key (or p_target equals start_key).
  walk(cur) AS (
    SELECT lower(p_target)
    UNION
    SELECT e.dst FROM walk w JOIN edges e ON e.src = w.cur
  )
  SELECT EXISTS (
    SELECT 1 FROM walk w JOIN start_key s ON w.cur = s.s
  );
$$;

-- SECURITY DEFINER is required because the caller (urmail_app) is subject to
-- RLS and would otherwise only see its own tenant's edges. The function runs
-- as urmail_platform (BYPASSRLS) so the walk covers the whole graph; it
-- returns a boolean and takes only scalar text parameters, so it cannot leak
-- other tenants' rows through the definer context.
ALTER FUNCTION urmail_alias_would_cycle(text, text, text) OWNER TO urmail_platform;

GRANT EXECUTE ON FUNCTION urmail_alias_would_cycle(text, text, text) TO urmail_app;
