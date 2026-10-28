DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM domain_dns_security WHERE dnssec_enabled OR secondary_hostname<>'') THEN
  RAISE EXCEPTION 'Disable DNSSEC only after safely removing parent DS, and remove secondary intent before downgrade';
 END IF;
END $$;
DROP TABLE domain_dns_security;
