-- Identity and tax disclosure are frozen alongside the confirmed quote, not
-- reconstructed from current tenant/profile settings during a later download.
ALTER TABLE billing_quotes ADD COLUMN document_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE invoices ADD COLUMN document_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb;
CREATE FUNCTION urmail_invoice_document_immutable() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF OLD.document_snapshot<>'{}'::jsonb AND (NEW.document_snapshot IS DISTINCT FROM OLD.document_snapshot OR NEW.amount_cents<>OLD.amount_cents OR NEW.currency<>OLD.currency OR NEW.tenant_id<>OLD.tenant_id OR NEW.order_id IS DISTINCT FROM OLD.order_id OR NEW.number<>OLD.number OR NEW.period_start IS DISTINCT FROM OLD.period_start OR NEW.period_end IS DISTINCT FROM OLD.period_end OR (OLD.issued_at IS NOT NULL AND NEW.issued_at IS DISTINCT FROM OLD.issued_at)) THEN
  RAISE EXCEPTION 'INVOICE_SNAPSHOT_IMMUTABLE' USING ERRCODE='23514';
 END IF;
 IF NEW.document_snapshot IS DISTINCT FROM OLD.document_snapshot AND current_setting('role',true) NOT IN('urmail_platform','urmail_migrate') THEN
  RAISE EXCEPTION 'INVOICE_SNAPSHOT_FORBIDDEN' USING ERRCODE='42501';
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_invoice_document_immutable() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_invoice_document_immutable() FROM PUBLIC;
CREATE TRIGGER invoice_document_immutable BEFORE UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION urmail_invoice_document_immutable();
-- Billing identity profiles are private to billing-capable tenant users and
-- are excluded from the general merged-settings API.
CREATE POLICY billing_document_profile_read ON settings AS RESTRICTIVE FOR SELECT USING (
 key<>'billing_document_profile' OR (scope='tenant' AND scope_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=scope_id AND user_id=nullif(current_setting('urmail.user_id',true),'')::bigint AND role IN('owner','admin','billing')))
);
CREATE FUNCTION urmail_billing_profile_write_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE row_value settings%ROWTYPE;
BEGIN
 IF TG_OP='DELETE' THEN row_value:=OLD; ELSE row_value:=NEW; END IF;
 IF TG_OP='UPDATE' AND OLD.key='billing_document_profile' AND (NEW.key<>OLD.key OR NEW.scope<>OLD.scope OR NEW.scope_id<>OLD.scope_id) THEN
  RAISE EXCEPTION 'BILLING_PROFILE_IDENTITY_IMMUTABLE' USING ERRCODE='42501';
 END IF;
 IF row_value.key='billing_document_profile' AND current_setting('role',true) NOT IN('urmail_platform','urmail_migrate') THEN
  IF row_value.scope<>'tenant' OR row_value.scope_id IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=row_value.scope_id AND user_id=nullif(current_setting('urmail.user_id',true),'')::bigint AND role IN('owner','admin','billing')) THEN
   RAISE EXCEPTION 'BILLING_PROFILE_FORBIDDEN' USING ERRCODE='42501';
  END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
ALTER FUNCTION urmail_billing_profile_write_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_billing_profile_write_guard() FROM PUBLIC;
CREATE TRIGGER billing_profile_write_guard BEFORE INSERT OR UPDATE OR DELETE ON settings FOR EACH ROW EXECUTE FUNCTION urmail_billing_profile_write_guard();
