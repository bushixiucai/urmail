ALTER TABLE refunds ADD COLUMN requested_amount_cents bigint;
UPDATE refunds SET requested_amount_cents=amount_cents;
ALTER TABLE refunds ALTER COLUMN requested_amount_cents SET NOT NULL;
ALTER TABLE refunds ALTER COLUMN requested_amount_cents SET DEFAULT 0;
ALTER TABLE refunds ADD COLUMN approved_by bigint REFERENCES users(id),
 ADD COLUMN approved_at timestamptz,
 ADD COLUMN approval_reason text,
 ADD COLUMN entitlement_action text NOT NULL DEFAULT 'none' CHECK(entitlement_action IN('none','revoke_future','terminate_current')),
 ADD COLUMN entitlement_effective_at timestamptz,
 ADD COLUMN target_term_order_id bigint,
 ADD COLUMN entitlement_applied_at timestamptz,
 ADD COLUMN last_error_code text,
 ADD COLUMN request_started_at timestamptz;
ALTER TABLE refunds ADD CONSTRAINT refund_target_term_fk FOREIGN KEY(tenant_id,target_term_order_id) REFERENCES subscription_terms(tenant_id,order_id);
ALTER TABLE refunds DROP CONSTRAINT refunds_status_check;
ALTER TABLE refunds ADD CONSTRAINT refunds_status_check CHECK(status IN('pending','approved','processing','completed','rejected','failed','uncertain'));
DROP INDEX refunds_payment_outstanding_idx;
CREATE INDEX refunds_payment_outstanding_idx ON refunds(tenant_id,payment_id) WHERE status IN('pending','approved','processing','completed','uncertain');
ALTER TABLE subscription_terms ADD COLUMN revoked_at timestamptz, ADD COLUMN revoked_by_refund bigint REFERENCES refunds(id);
ALTER TABLE invoices ADD COLUMN source_payment_id bigint;
ALTER TABLE invoices ADD CONSTRAINT invoice_source_payment_fk FOREIGN KEY(source_payment_id,tenant_id) REFERENCES payments(id,tenant_id);
ALTER TABLE commissions ADD COLUMN source_payment_id bigint;
ALTER TABLE commissions ADD CONSTRAINT commission_source_payment_fk FOREIGN KEY(source_payment_id,tenant_id) REFERENCES payments(id,tenant_id);
-- Only uniquely attributable historical payments are linked; ambiguous history
-- remains unchanged for explicit operator reconciliation.
UPDATE invoices i SET source_payment_id=p.id FROM (SELECT order_id,min(id)id FROM payments WHERE status='succeeded' GROUP BY order_id HAVING count(*)=1)p WHERE p.order_id=i.order_id;
UPDATE commissions c SET source_payment_id=p.id FROM (SELECT order_id,min(id)id FROM payments WHERE status='succeeded' GROUP BY order_id HAVING count(*)=1)p WHERE p.order_id=c.order_id;
CREATE TABLE commission_refund_adjustments (
 refund_id bigint NOT NULL REFERENCES refunds(id),
 commission_id bigint NOT NULL REFERENCES commissions(id),
 amount_cents bigint NOT NULL CHECK(amount_cents>=0),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(refund_id,commission_id)
);
ALTER TABLE commission_refund_adjustments OWNER TO urmail_platform;
REVOKE ALL ON commission_refund_adjustments FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE commission_refund_adjustments ENABLE ROW LEVEL SECURITY;
CREATE POLICY no_app_access ON commission_refund_adjustments USING(false);
UPDATE refunds SET status='uncertain',last_error_code='legacy_approval_unverified' WHERE status IN('approved','processing') AND approved_by IS NULL;
CREATE FUNCTION urmail_refund_approval_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE paid payments%ROWTYPE; reserved numeric; actor bigint;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'REFUND_IMMUTABLE' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(NEW.tenant_id);
 SELECT * INTO paid FROM payments WHERE tenant_id=NEW.tenant_id AND id=NEW.payment_id FOR UPDATE;
 IF NOT FOUND OR paid.order_id IS DISTINCT FROM NEW.order_id OR paid.currency<>NEW.currency OR NEW.amount_cents<=0 THEN RAISE EXCEPTION 'REFUND_PAYMENT_NOT_FOUND' USING ERRCODE='23514'; END IF;
 IF current_setting('role',true) NOT IN('urmail_platform','urmail_migrate') THEN
  actor:=nullif(current_setting('urmail.user_id',true),'')::bigint;
  IF TG_OP<>'INSERT' OR NEW.status<>'pending' OR NEW.approved_by IS NOT NULL OR NEW.entitlement_action<>'none' OR NEW.tenant_id IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=NEW.tenant_id AND user_id=actor AND role IN('owner','admin','billing')) THEN RAISE EXCEPTION 'REFUND_APPROVAL_FORBIDDEN' USING ERRCODE='42501'; END IF;
 END IF;
 IF TG_OP='INSERT' THEN NEW.requested_amount_cents:=NEW.amount_cents; END IF;
 IF NEW.status IN('approved','processing','completed','uncertain') AND (NEW.approved_by IS NULL OR length(trim(NEW.approval_reason))<10 OR NOT EXISTS(SELECT 1 FROM platform_admins WHERE user_id=NEW.approved_by)) THEN RAISE EXCEPTION 'REFUND_APPROVAL_REQUIRED' USING ERRCODE='23514'; END IF;
 IF NEW.provider_refund_no IS NOT NULL THEN
  PERFORM pg_advisory_xact_lock(4011,hashtext(paid.provider||':'||paid.account_scope||':'||NEW.provider_refund_no));
  IF EXISTS(SELECT 1 FROM refunds r JOIN payments rp ON rp.tenant_id=r.tenant_id AND rp.id=r.payment_id WHERE r.id<>NEW.id AND r.provider_refund_no=NEW.provider_refund_no AND rp.provider=paid.provider AND rp.account_scope=paid.account_scope) THEN RAISE EXCEPTION 'REFUND_PROVIDER_REFERENCE_CONFLICT' USING ERRCODE='23514'; END IF;
 END IF;
 IF TG_OP='UPDATE' AND OLD.status='completed' AND (NEW.amount_cents<>OLD.amount_cents OR NEW.status<>OLD.status OR NEW.entitlement_action<>OLD.entitlement_action OR NEW.entitlement_effective_at IS DISTINCT FROM OLD.entitlement_effective_at OR NEW.target_term_order_id IS DISTINCT FROM OLD.target_term_order_id) THEN RAISE EXCEPTION 'REFUND_IMMUTABLE' USING ERRCODE='23514'; END IF;
 IF NEW.entitlement_action<>'none' AND (NEW.target_term_order_id IS DISTINCT FROM NEW.order_id OR NEW.entitlement_effective_at IS NULL OR NOT EXISTS(SELECT 1 FROM subscription_terms WHERE tenant_id=NEW.tenant_id AND order_id=NEW.target_term_order_id AND basis_order_id IS NULL)) THEN RAISE EXCEPTION 'REFUND_ENTITLEMENT_TARGET_INVALID' USING ERRCODE='23514'; END IF;
 IF NEW.status='approved' AND NEW.entitlement_action='revoke_future' AND NOT EXISTS(SELECT 1 FROM subscription_terms WHERE tenant_id=NEW.tenant_id AND order_id=NEW.target_term_order_id AND period_start>now()) THEN RAISE EXCEPTION 'REFUND_FUTURE_TERM_REQUIRED' USING ERRCODE='23514'; END IF;
 IF NEW.status='approved' AND NEW.entitlement_action='terminate_current' AND NOT EXISTS(SELECT 1 FROM subscription_terms WHERE tenant_id=NEW.tenant_id AND order_id=NEW.target_term_order_id AND period_start<=now() AND period_end>now()) THEN RAISE EXCEPTION 'REFUND_CURRENT_TERM_REQUIRED' USING ERRCODE='23514'; END IF;
 IF NEW.entitlement_action<>'none' AND EXISTS(SELECT 1 FROM subscription_terms a JOIN payments ap ON ap.tenant_id=a.tenant_id AND ap.order_id=a.order_id WHERE a.tenant_id=NEW.tenant_id AND a.basis_order_id=NEW.target_term_order_id AND ap.amount_cents>(SELECT COALESCE(sum(amount_cents),0) FROM refunds WHERE tenant_id=NEW.tenant_id AND payment_id=ap.id AND status='completed')) THEN RAISE EXCEPTION 'REFUND_ADJUSTMENTS_OUTSTANDING' USING ERRCODE='23514'; END IF;
 IF NEW.entitlement_action<>'none' AND NOT EXISTS(SELECT 1 FROM invoices i WHERE i.tenant_id=NEW.tenant_id AND i.order_id=NEW.target_term_order_id AND i.source_payment_id=NEW.payment_id) THEN RAISE EXCEPTION 'REFUND_ENTITLEMENT_TARGET_INVALID' USING ERRCODE='23514'; END IF;
 IF NEW.entitlement_action<>'none' AND NEW.amount_cents<>paid.amount_cents THEN RAISE EXCEPTION 'REFUND_FULL_PERIOD_REQUIRED' USING ERRCODE='23514'; END IF;
 SELECT COALESCE(sum(amount_cents),0) INTO reserved FROM refunds WHERE tenant_id=NEW.tenant_id AND payment_id=NEW.payment_id AND id<>NEW.id AND status IN('pending','approved','processing','completed','uncertain');
 IF NEW.status IN('pending','approved','processing','completed','uncertain') AND reserved+NEW.amount_cents>paid.amount_cents THEN RAISE EXCEPTION 'REFUND_EXCEEDS_PAYMENT' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_refund_approval_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_refund_approval_guard() FROM PUBLIC;
CREATE TRIGGER refund_approval_guard BEFORE INSERT OR UPDATE OR DELETE ON refunds FOR EACH ROW EXECUTE FUNCTION urmail_refund_approval_guard();
REVOKE UPDATE,DELETE ON refunds FROM urmail_app;
-- Payments are immutable to the app role; reservation serialization is owned
-- by the existing advisory lock and the platform trigger's row lock.
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_create_refund(bigint,bigint,bigint,text,bigint,text,text)'::regprocedure) INTO definition;
 definition:=replace(definition,'FOR UPDATE;',';');
 definition:=replace(definition,'PERFORM pg_advisory_xact_lock(p_payment_id);','PERFORM pg_advisory_xact_lock(session_tenant);');
 definition:=replace(definition,'''pending'',''approved'',''processing'',''completed''','''pending'',''approved'',''processing'',''completed'',''uncertain''');
 EXECUTE definition;
END $$;
-- A refunded current base period does not erase later unrefunded paid terms.
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_effective_plan(bigint)'::regprocedure) INTO definition;
 definition:=replace(definition,'term.period_end>now()','term.period_end>now() AND (term.revoked_at IS NULL OR term.revoked_at>now())');
 EXECUTE definition;
END $$;
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 definition:=replace(definition,'s.period_end>now()','s.period_end>now() AND /* refund_term_gate */ (NOT EXISTS(SELECT 1 FROM subscription_terms st WHERE st.tenant_id=s.tenant_id) OR EXISTS(SELECT 1 FROM subscription_terms st WHERE st.tenant_id=s.tenant_id AND st.period_start<=now() AND st.period_end>now() AND (st.revoked_at IS NULL OR st.revoked_at>now())))');
 IF strpos(definition,'refund_term_gate')=0 THEN RAISE EXCEPTION 'MAIL_ENTITLEMENT_GATE_UPDATE_REQUIRED'; END IF;
 EXECUTE definition;
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'SELECT * INTO p FROM urmail_effective_plan(tid);','IF EXISTS(SELECT 1 FROM subscription_terms st WHERE st.tenant_id=tid) AND NOT EXISTS(SELECT 1 FROM subscription_terms st WHERE st.tenant_id=tid AND st.period_start<=now() AND st.period_end>now() AND (st.revoked_at IS NULL OR st.revoked_at>now())) THEN RAISE EXCEPTION ''SUBSCRIPTION_EXPIRED'' USING ERRCODE=''23514''; END IF; SELECT * INTO p FROM urmail_effective_plan(tid);');
 EXECUTE definition;
END $$;

DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_redeem_trial(bigint,bigint,text,text)'::regprocedure) INTO definition;
 definition:=replace(definition,'SELECT 1 FROM subscription_terms WHERE tenant_id=p_tenant AND period_end>now()', 'SELECT 1 FROM subscription_terms WHERE tenant_id=p_tenant AND period_end>now() AND (revoked_at IS NULL OR revoked_at>now())');
 EXECUTE definition;
 SELECT pg_get_functiondef('urmail_subscription_term_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'AND t.basis_order_id IS NULL AND t.period_start<NEW.period_end','AND t.basis_order_id IS NULL AND t.revoked_at IS NULL AND t.period_start<NEW.period_end');
 EXECUTE definition;
END $$;
