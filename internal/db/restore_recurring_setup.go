package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// holdRestoredRecurringSetups invalidates all unconsumed hosted proofs. Saved
// bindings and confirmed enrollment/financial facts follow their existing
// recovery fences; restoring never creates a session or charges an instrument.
func holdRestoredRecurringSetups(ctx context.Context, tx pgx.Tx, restoreID string, held map[string]int64) error {
	columns, e := restoreColumns(ctx, tx, "recurring_setup_intents")
	if e != nil {
		return e
	}
	if !columns["request_id"] || !columns["hosted_url"] {
		return nil
	}
	_, e = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,actor_user_id,action,target_type,target_id,detail)
 SELECT tenant_id,actor_id,'restore.external_work_held','recurring_setup',id::text,jsonb_build_object('restore_id',$1::text,'previous_state',state,'previous_version',version,'request_id',request_id,'merchant_id',merchant_id)
 FROM recurring_setup_intents WHERE state IN('creating','ready','uncertain')`, restoreID)
	if e != nil {
		return e
	}
	result, e := tx.Exec(ctx, `UPDATE recurring_setup_intents SET state='held',version=version+1,hosted_url='',expires_at=least(expires_at,now()),last_error=$1,updated_at=now()
 WHERE state IN('creating','ready','uncertain')`, restoreReviewCode)
	if e != nil {
		return e
	}
	held["recurring_setup_intents"] = result.RowsAffected()
	return nil
}
