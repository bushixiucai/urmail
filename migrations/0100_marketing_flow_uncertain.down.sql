DROP FUNCTION IF EXISTS urmail_mark_marketing_flow_uncertain(bigint,bigint,text);
ALTER TABLE marketing_flow_positions DROP COLUMN IF EXISTS delivery_uncertain;
