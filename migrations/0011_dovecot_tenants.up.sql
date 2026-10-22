-- Dovecot's userdb/passdb queries join tenants to reject suspended tenants.
-- Grant only the columns needed by those queries; keep BYPASSRLS on the
-- dedicated role so it can serve all tenants.
GRANT SELECT ON tenants TO urmail_dovecot;
