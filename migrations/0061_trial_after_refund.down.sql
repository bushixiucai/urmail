DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 definition:=replace(definition,'/* refund_term_gate */ (/* trial_after_refund */ (s.status=''trialing'' AND EXISTS(SELECT 1 FROM tenant_trials tt WHERE tt.tenant_id=s.tenant_id AND tt.started_at<=now() AND tt.ends_at>now())) OR ','/* refund_term_gate */ (');
 EXECUTE definition;
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'IF NOT EXISTS(SELECT 1 FROM subscriptions ts JOIN tenant_trials tt ON tt.tenant_id=ts.tenant_id WHERE ts.tenant_id=tid AND ts.status=''trialing'' AND ts.period_start<=now() AND ts.period_end>now() AND tt.started_at<=now() AND tt.ends_at>now()) AND /* trial_after_refund */ EXISTS(SELECT 1 FROM subscription_terms st WHERE st.tenant_id=tid) AND NOT EXISTS(',
 'IF EXISTS(SELECT 1 FROM subscription_terms st WHERE st.tenant_id=tid) AND NOT EXISTS(');
 EXECUTE definition;
END $$;
