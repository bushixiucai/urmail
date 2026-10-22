-- 0001_init rollback.
--
-- Drops everything created by 0001_init.up.sql. Roles are intentionally left
-- in place: they may be shared with other databases on the same server, and
-- dropping them would break an unrelated installation.

DROP TABLE IF EXISTS audit_logs CASCADE;
DROP TABLE IF EXISTS recall_requests CASCADE;
DROP TABLE IF EXISTS archive_entries CASCADE;
DROP TABLE IF EXISTS commissions CASCADE;
DROP TABLE IF EXISTS coupon_redemptions CASCADE;
DROP TABLE IF EXISTS coupons CASCADE;
DROP TABLE IF EXISTS invoices CASCADE;
DROP TABLE IF EXISTS payments CASCADE;
DROP TABLE IF EXISTS payment_events CASCADE;
DROP TABLE IF EXISTS order_items CASCADE;
DROP TABLE IF EXISTS orders CASCADE;
DROP TABLE IF EXISTS channel_attribution CASCADE;
DROP TABLE IF EXISTS channels CASCADE;
DROP TABLE IF EXISTS subscriptions CASCADE;
DROP TABLE IF EXISTS plans CASCADE;
DROP TABLE IF EXISTS mailbox_usage CASCADE;
DROP TABLE IF EXISTS mailbox_aliases CASCADE;
DROP TABLE IF EXISTS mailboxes CASCADE;
DROP TABLE IF EXISTS domains CASCADE;
DROP TABLE IF EXISTS settings CASCADE;
DROP TABLE IF EXISTS user_tenant_roles CASCADE;
DROP TABLE IF EXISTS platform_admins CASCADE;
DROP TABLE IF EXISTS ticket_messages CASCADE;
DROP TABLE IF EXISTS tickets CASCADE;
DROP TABLE IF EXISTS announcements CASCADE;
DROP TABLE IF EXISTS sessions CASCADE;
DROP TABLE IF EXISTS api_tokens CASCADE;
DROP TABLE IF EXISTS users CASCADE;
DROP TABLE IF EXISTS tenants CASCADE;
DROP TABLE IF EXISTS brands CASCADE;

-- The RLS policies and table ownership were dropped with the tables above.
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM urmail_app;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM urmail_app;
