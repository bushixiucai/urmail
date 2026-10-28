package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// holdRestoredMailboxImports retains accepted receipts and sealed payloads but
// invalidates old previews, leases and negative presence observations.
func holdRestoredMailboxImports(ctx context.Context, tx pgx.Tx, restoreID string, held map[string]int64) error {
	jobs, err := restoreColumns(ctx, tx, "mailbox_import_jobs")
	if err != nil {
		return err
	}
	items, err := restoreColumns(ctx, tx, "mailbox_import_items")
	if err != nil {
		return err
	}
	if !jobs["preview_expires_at"] || !jobs["lease_token"] || !items["reconcile_absent"] {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,action,target_type,target_id,detail)
 SELECT tenant_id,'restore.external_work_held','mailbox_import',id::text,jsonb_build_object('restore_id',$1::text,'mailbox_id',mailbox_id,'previous_state',status,'previous_version',version,'source_digest',source_digest,'preview_digest',preview_digest)
 FROM mailbox_import_jobs WHERE status NOT IN('completed','canceled') AND (status<>'held' OR lease_token<>'' OR lease_until IS NOT NULL OR preview_expires_at>now())`, restoreID)
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE mailbox_import_jobs SET status='held',preview_expires_at=least(preview_expires_at,now()),version=version+1,lease_token='',lease_until=NULL,updated_at=now()
 WHERE status NOT IN('completed','canceled') AND (status<>'held' OR lease_token<>'' OR lease_until IS NOT NULL OR preview_expires_at>now())`)
	if err != nil {
		return err
	}
	held["mailbox_import_jobs"] = result.RowsAffected()
	result, err = tx.Exec(ctx, `UPDATE mailbox_import_items SET
 status=CASE WHEN status IN('pending','importing') THEN 'unknown' ELSE status END,
 error_code=CASE WHEN status IN('pending','importing') THEN $1 ELSE error_code END,
 reconciled_at=NULL,reconcile_absent=false,updated_at=now()
 WHERE status IN('pending','importing') OR reconciled_at IS NOT NULL OR reconcile_absent`, restoreReviewCode)
	if err != nil {
		return err
	}
	held["mailbox_import_items.observations"] = result.RowsAffected()
	return nil
}
