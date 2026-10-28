package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// invalidateRestoredBrandHostProofs keeps the brand/hostname intent but requires
// fresh ownership and certificate verification before serving brand traffic.
func invalidateRestoredBrandHostProofs(ctx context.Context, tx pgx.Tx, held map[string]int64) error {
	columns, err := restoreColumns(ctx, tx, "brand_hosts")
	if err != nil {
		return err
	}
	if columns["status"] && columns["verified_at"] && columns["cert_issued_at"] && columns["activated_at"] {
		result, e := tx.Exec(ctx, `UPDATE brand_hosts SET status='pending-verify',verified_at=NULL,cert_issued_at=NULL,activated_at=NULL,failure_reason='RESTORE_RECONCILIATION_REQUIRED',updated_at=now() WHERE status<>'pending-verify' OR verified_at IS NOT NULL OR cert_issued_at IS NOT NULL OR activated_at IS NOT NULL`)
		if e != nil {
			return e
		}
		held["brand_hosts.ownership_proof"] = result.RowsAffected()
	}
	challengeColumns, err := restoreColumns(ctx, tx, "brand_host_challenges")
	if err != nil {
		return err
	}
	if challengeColumns["token_digest"] {
		result, e := tx.Exec(ctx, `DELETE FROM brand_host_challenges`)
		if e != nil {
			return e
		}
		held["brand_host_challenges.invalidated"] = result.RowsAffected()
	}
	return nil
}
