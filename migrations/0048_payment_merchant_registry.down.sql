DROP TRIGGER payment_inbox_alert ON payment_webhook_inbox;
DROP FUNCTION urmail_payment_inbox_alert();
-- Refuse lossy rollback if distinct merchant accounts reused an external ID.
DROP TABLE payment_webhook_inbox;
ALTER TABLE payment_events DROP CONSTRAINT payment_events_provider_scope_event_unique;
ALTER TABLE payment_events ADD CONSTRAINT payment_events_provider_provider_event_id_key UNIQUE(provider,provider_event_id);
DROP INDEX payments_provider_txn_uniq;
CREATE UNIQUE INDEX payments_provider_txn_uniq ON payments(provider,provider_txn_id);
ALTER TABLE payment_events DROP COLUMN account_scope;
ALTER TABLE payments DROP COLUMN account_scope;
DROP TABLE payment_merchants;

GRANT INSERT,UPDATE,DELETE ON payments TO urmail_app;
