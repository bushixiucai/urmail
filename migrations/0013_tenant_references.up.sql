-- PostgreSQL 14: retain the scalar SET NULL actions; deferred composite checks
-- verify tenant consistency after those actions without nulling tenant_id.
-- Refuse existing mismatches; never change tenant ownership to repair data.
DO $$
DECLARE spec text[]; bad bigint; failures text := '';
BEGIN
 FOREACH spec SLICE 1 IN ARRAY ARRAY[
 ['archive_entries','mailbox_id','mailboxes'],['mailbox_usage','mailbox_id','mailboxes'],
 ['order_items','order_id','orders'],['payments','order_id','orders'],
 ['coupon_redemptions','order_id','orders'],['commissions','order_id','orders']]
 LOOP
  EXECUTE format('SELECT count(*) FROM %I c JOIN %I p ON p.id=c.%I WHERE c.tenant_id<>p.tenant_id',spec[1],spec[3],spec[2]) INTO bad;
  IF bad>0 THEN failures:=failures||format('%s.%s: %s mismatched rows; ',spec[1],spec[2],bad); END IF;
 END LOOP;
 IF failures<>'' THEN RAISE EXCEPTION 'TENANT_REFERENCE_PREFLIGHT: %Run urmailctl data-check, review IDs, and resolve ownership before retrying; no rows were changed.',failures; END IF;
END $$;
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_id_tenant_unique UNIQUE(id,tenant_id);
ALTER TABLE orders ADD CONSTRAINT orders_id_tenant_unique UNIQUE(id,tenant_id);
ALTER TABLE archive_entries ADD CONSTRAINT archive_mailbox_tenant_fk FOREIGN KEY(mailbox_id,tenant_id) REFERENCES mailboxes(id,tenant_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED NOT VALID;
ALTER TABLE mailbox_usage DROP CONSTRAINT mailbox_usage_mailbox_id_fkey, ADD CONSTRAINT usage_mailbox_tenant_fk FOREIGN KEY(mailbox_id,tenant_id) REFERENCES mailboxes(id,tenant_id) ON DELETE CASCADE NOT VALID;
ALTER TABLE order_items DROP CONSTRAINT order_items_order_id_fkey, ADD CONSTRAINT items_order_tenant_fk FOREIGN KEY(order_id,tenant_id) REFERENCES orders(id,tenant_id) ON DELETE CASCADE NOT VALID;
ALTER TABLE payments ADD CONSTRAINT payments_order_tenant_fk FOREIGN KEY(order_id,tenant_id) REFERENCES orders(id,tenant_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED NOT VALID;
ALTER TABLE coupon_redemptions ADD CONSTRAINT coupons_order_tenant_fk FOREIGN KEY(order_id,tenant_id) REFERENCES orders(id,tenant_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED NOT VALID;
ALTER TABLE commissions DROP CONSTRAINT commissions_order_id_fkey, ADD CONSTRAINT commissions_order_tenant_fk FOREIGN KEY(order_id,tenant_id) REFERENCES orders(id,tenant_id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE archive_entries VALIDATE CONSTRAINT archive_mailbox_tenant_fk;
ALTER TABLE mailbox_usage VALIDATE CONSTRAINT usage_mailbox_tenant_fk;
ALTER TABLE order_items VALIDATE CONSTRAINT items_order_tenant_fk;
ALTER TABLE payments VALIDATE CONSTRAINT payments_order_tenant_fk;
ALTER TABLE coupon_redemptions VALIDATE CONSTRAINT coupons_order_tenant_fk;
ALTER TABLE commissions VALIDATE CONSTRAINT commissions_order_tenant_fk;
