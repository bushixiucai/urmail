CREATE OR REPLACE FUNCTION urmail_refund_approval_guard() RETURNS trigger
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

-- Removing retry dates never converts processing or uncertain refunds to approved.
DROP INDEX refunds_entitlement_due;
DROP INDEX refunds_reconcile_due;
DROP INDEX refunds_dispatch_due;
ALTER TABLE refunds DROP COLUMN next_entitlement_attempt_at,DROP COLUMN next_reconcile_at,DROP COLUMN dispatch_attempts,DROP COLUMN next_attempt_at;
