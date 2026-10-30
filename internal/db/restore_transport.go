package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// invalidateRestoredTransportObservations preserves the already published
// policy and retained report facts, but a rewound DNS/TLS observation cannot
// authorize a future transition from testing to enforcement.
func invalidateRestoredTransportObservations(ctx context.Context, tx pgx.Tx, held map[string]int64) error {
	columns, err := restoreColumns(ctx, tx, "domain_transport_security")
	if err != nil {
		return err
	}
	if !columns["observed_at"] || !columns["observation_digest"] || !columns["observation_status"] || !columns["observation_detail"] {
		return nil
	}
	result, err := tx.Exec(ctx, `UPDATE domain_transport_security SET observed_at=NULL,observation_digest=NULL,observation_status='unverified',observation_detail='{}'::jsonb WHERE observed_at IS NOT NULL OR observation_digest IS NOT NULL OR observation_status<>'unverified' OR observation_detail<>'{}'::jsonb`)
	if err != nil {
		return err
	}
	held["domain_transport_security.observations"] = result.RowsAffected()
	return nil
}

// holdRestoredScheduledSentCopies preserves exact accepted payloads but forbids
// old credentials from APPENDing until the owner explicitly reviews/repairs.
func holdRestoredScheduledSentCopies(ctx context.Context, tx pgx.Tx, held map[string]int64) error {
	columns, err := restoreColumns(ctx, tx, "mailbox_scheduled_sends")
	if err != nil {
		return err
	}
	if !columns["sent_copy_state"] {
		return nil
	}
	rawPredicate := "sent_raw IS NOT NULL"
	if columns["sent_raw_secret"] {
		rawPredicate = "(sent_raw IS NOT NULL OR sent_raw_secret<>'')"
	}
	result, err := tx.Exec(ctx, `UPDATE mailbox_scheduled_sends SET sent_copy_state='held',smtp_secret='',lease_until=NULL,claim_token=claim_token+1,last_error='RESTORE_RECONCILIATION_REQUIRED',updated_at=now() WHERE status='sent' AND `+rawPredicate+` AND sent_copy_state IN('pending','unknown','held')`)
	if err != nil {
		return err
	}
	held["mailbox_scheduled_sends.sent_copy"] = result.RowsAffected()
	return nil
}

// holdRestoredImmediateSends prevents replay of restored SMTP intentions and credentials.
func holdRestoredImmediateSends(ctx context.Context, tx pgx.Tx, held map[string]int64) error {
	columns, err := restoreColumns(ctx, tx, "mailbox_send_receipts")
	if err != nil {
		return err
	}
	if !columns["status"] {
		return nil
	}
	result, err := tx.Exec(ctx, `UPDATE mailbox_send_receipts SET status=CASE WHEN status IN('prepared','submitting') THEN 'uncertain' ELSE status END,sent_copy_state=CASE WHEN status='accepted' AND sent_copy_state<>'complete' THEN 'held' ELSE sent_copy_state END,smtp_secret='',claim_token=claim_token+1,lease_until=NULL,last_error='RESTORE_RECONCILIATION_REQUIRED',updated_at=now() WHERE status IN('prepared','submitting','uncertain') OR (status='accepted' AND sent_copy_state IN('pending','unknown','held'))`)
	if err != nil {
		return err
	}
	held["mailbox_send_receipts"] = result.RowsAffected()
	return nil
}
