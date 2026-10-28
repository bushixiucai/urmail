-- Queue rows and their delivery/fencing facts are unaffected by this rollback.
DROP TRIGGER IF EXISTS tenant_marketing_dispatch ON tenants;
DROP FUNCTION IF EXISTS urmail_seed_marketing_dispatch();
DROP TABLE IF EXISTS platform_marketing_dispatch;
DROP SEQUENCE IF EXISTS platform_marketing_dispatch_turn_seq;
