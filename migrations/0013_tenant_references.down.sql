ALTER TABLE archive_entries DROP CONSTRAINT archive_mailbox_tenant_fk;
ALTER TABLE mailbox_usage DROP CONSTRAINT usage_mailbox_tenant_fk, ADD FOREIGN KEY(mailbox_id) REFERENCES mailboxes(id) ON DELETE CASCADE;
ALTER TABLE order_items DROP CONSTRAINT items_order_tenant_fk, ADD FOREIGN KEY(order_id) REFERENCES orders(id) ON DELETE CASCADE;
ALTER TABLE payments DROP CONSTRAINT payments_order_tenant_fk;
ALTER TABLE coupon_redemptions DROP CONSTRAINT coupons_order_tenant_fk;
ALTER TABLE commissions DROP CONSTRAINT commissions_order_tenant_fk, ADD FOREIGN KEY(order_id) REFERENCES orders(id) ON DELETE RESTRICT;
ALTER TABLE orders DROP CONSTRAINT orders_id_tenant_unique;
ALTER TABLE mailboxes DROP CONSTRAINT mailboxes_id_tenant_unique;
