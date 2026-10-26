-- Diagnostic metadata is disposable; financial event inbox rows are untouched.
DROP FUNCTION urmail_record_rejected_webhook(bigint,text,text,integer);
DROP TABLE payment_webhook_rejections;
DROP TABLE payment_webhook_rejection_budget;
