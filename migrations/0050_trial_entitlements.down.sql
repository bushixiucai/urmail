DROP TRIGGER trial_redemption_guard ON coupon_redemptions;
DROP FUNCTION urmail_trial_redemption_guard();
DROP FUNCTION urmail_redeem_trial(bigint,bigint,text,text);
DROP TABLE tenant_trials;
