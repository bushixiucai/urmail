DROP TRIGGER billing_profile_write_guard ON settings;
DROP FUNCTION urmail_billing_profile_write_guard();
DROP POLICY billing_document_profile_read ON settings;
DROP TRIGGER invoice_document_immutable ON invoices;
DROP FUNCTION urmail_invoice_document_immutable();
ALTER TABLE invoices DROP COLUMN document_snapshot;
ALTER TABLE billing_quotes DROP COLUMN document_snapshot;
