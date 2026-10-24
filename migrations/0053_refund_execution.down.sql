DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_redeem_trial(bigint,bigint,text,text)'::regprocedure) INTO definition;
 definition:=replace(definition,'SELECT 1 FROM subscription_terms WHERE tenant_id=p_tenant AND period_end>now() AND (revoked_at IS NULL OR revoked_at>now())','SELECT 1 FROM subscription_terms WHERE tenant_id=p_tenant AND period_end>now()');
 EXECUTE definition;
 SELECT pg_get_functiondef('urmail_subscription_term_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'AND t.basis_order_id IS NULL AND t.revoked_at IS NULL AND t.period_start<NEW.period_end','AND t.basis_order_id IS NULL AND t.period_start<NEW.period_end');
 EXECUTE definition;
END $$;
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 definition:=replace(definition,' AND /* refund_term_gate */ (NOT EXISTS(SELECT 1 FROM subscription_terms st WHERE st.tenant_id=s.tenant_id) OR EXISTS(SELECT 1 FROM subscription_terms st WHERE st.tenant_id=s.tenant_id AND st.period_start<=now() AND st.period_end>now() AND (st.revoked_at IS NULL OR st.revoked_at>now())))','');
 EXECUTE definition;
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'IF EXISTS(SELECT 1 FROM subscription_terms st WHERE st.tenant_id=tid) AND NOT EXISTS(SELECT 1 FROM subscription_terms st WHERE st.tenant_id=tid AND st.period_start<=now() AND st.period_end>now() AND (st.revoked_at IS NULL OR st.revoked_at>now())) THEN RAISE EXCEPTION ''SUBSCRIPTION_EXPIRED'' USING ERRCODE=''23514''; END IF; ','');
 EXECUTE definition;
END $$;
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_effective_plan(bigint)'::regprocedure) INTO definition;
 definition:=replace(definition,'term.period_end>now() AND (term.revoked_at IS NULL OR term.revoked_at>now())','term.period_end>now()');
 EXECUTE definition;
END $$;
DROP TRIGGER refund_approval_guard ON refunds;
DROP FUNCTION urmail_refund_approval_guard();
DROP TABLE commission_refund_adjustments;
ALTER TABLE commissions DROP COLUMN source_payment_id;
ALTER TABLE invoices DROP COLUMN source_payment_id;
ALTER TABLE subscription_terms DROP COLUMN revoked_at,DROP COLUMN revoked_by_refund;
ALTER TABLE refunds DROP COLUMN requested_amount_cents,DROP COLUMN approved_by,DROP COLUMN approved_at,DROP COLUMN approval_reason,DROP COLUMN entitlement_action,DROP COLUMN entitlement_effective_at,DROP COLUMN target_term_order_id,DROP COLUMN entitlement_applied_at,DROP COLUMN last_error_code,DROP COLUMN request_started_at;
UPDATE refunds SET status='processing' WHERE status='uncertain';
ALTER TABLE refunds DROP CONSTRAINT refunds_status_check;
ALTER TABLE refunds ADD CONSTRAINT refunds_status_check CHECK(status IN('pending','approved','processing','completed','rejected','failed'));
DROP INDEX refunds_payment_outstanding_idx;
CREATE INDEX refunds_payment_outstanding_idx ON refunds(tenant_id,payment_id) WHERE status IN('pending','approved','processing','completed');
GRANT UPDATE,DELETE ON refunds TO urmail_app;
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_create_refund(bigint,bigint,bigint,text,bigint,text,text)'::regprocedure) INTO definition;
 definition:=replace(definition,'PERFORM pg_advisory_xact_lock(session_tenant);','PERFORM pg_advisory_xact_lock(p_payment_id);');
 definition:=replace(definition,'''pending'',''approved'',''processing'',''completed'',''uncertain''','''pending'',''approved'',''processing'',''completed''');
 EXECUTE definition;
END $$;
