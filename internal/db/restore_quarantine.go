package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const restoreReviewCode = "RESTORE_RECONCILIATION_REQUIRED"

type restoredWorkSpec struct {
	table, state, target string
	active               []string
}

var restoredWorkSpecs = []restoredWorkSpec{
	{"refunds", "status", "uncertain", []string{"pending", "approved", "processing", "uncertain"}},
	{"marketing_recipients", "status", "uncertain", []string{"queued", "claimed", "leased", "submitting", "uncertain"}},
	{"marketing_campaigns", "status", "paused", []string{"scheduled", "running"}},
	{"ai_usage_requests", "status", "uncertain", []string{"reserved", "inflight", "uncertain"}},
	{"payment_checkout_intents", "state", "uncertain", []string{"creating", "ready", "cancel_requested", "uncertain"}},
	{"payment_webhook_inbox", "state", "uncertain", []string{"received", "processing", "uncertain"}},
	{"notification_outbox", "state", "uncertain", []string{"pending", "uncertain"}},
	{"onboarding_tasks", "status", "blocked", []string{"pending", "running", "blocked"}},
	{"dns_change_plans", "status", "uncertain", []string{"pending", "confirmed", "applying", "rolling_back", "running", "uncertain"}},
	{"dns_record_actions", "status", "uncertain", []string{"pending", "applying", "submitted", "uncertain"}},
}

// HoldRestoredSideEffects runs after migrations while every writer remains
// stopped. It never submits, releases budgets, changes amounts or invents approvals.
func HoldRestoredSideEffects(ctx context.Context, databaseURL, restoreID string) error {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SET LOCAL ROLE urmail_platform"); err != nil {
		return err
	}
	if err = holdRestoredWork(ctx, tx, restoreID, true); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func restoreColumns(ctx context.Context, tx pgx.Tx, table string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name=$1`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}
func restoreStateSupported(ctx context.Context, tx pgx.Tx, table, column, target string) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid=to_regclass($1) AND contype='c'`, "public."+table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	relevant := false
	for rows.Next() {
		var definition string
		if err = rows.Scan(&definition); err != nil {
			return false, err
		}
		if strings.Contains(definition, column) {
			relevant = true
			if strings.Contains(definition, "'"+target+"'") {
				return true, nil
			}
		}
	}
	return !relevant, rows.Err()
}

func holdRestoredWork(ctx context.Context, tx pgx.Tx, restoreID string, strict bool) error {
	// Ownership was restored first. SET LOCAL cannot leak a privileged role back
	// into a runtime pool, and the migration credential must actually be a member.
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE urmail_platform"); err != nil {
		return err
	}
	held := map[string]int64{}
	unsupported := map[string]int64{}
	for _, spec := range restoredWorkSpecs {
		columns, err := restoreColumns(ctx, tx, spec.table)
		if err != nil {
			return err
		}
		if !columns[spec.state] {
			continue
		}
		table := pgx.Identifier{"public", spec.table}.Sanitize()
		state := pgx.Identifier{spec.state}.Sanitize()
		where := state + "::text=ANY($1::text[])"
		if spec.table == "notification_outbox" && columns["channel"] {
			where += " AND channel='email'"
		}
		var count int64
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+where, spec.active).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			continue
		}
		// Preserve the snapshot's original state as an append-only provenance fact.
		idColumn := "id"
		if !columns[idColumn] {
			idColumn = "order_id"
		}
		if columns[idColumn] {
			tenant := "NULL::bigint"
			if columns["tenant_id"] {
				tenant = "tenant_id"
			}
			query := "INSERT INTO audit_logs(tenant_id,action,target_type,target_id,detail) SELECT " + tenant + ",'restore.external_work_held','restore_work',$2||'/'||" + pgx.Identifier{idColumn}.Sanitize() + "::text,jsonb_build_object('restore_id',$3::text,'source_table',$2::text,'previous_state'," + state + ",'hold_state',$4::text) FROM " + table + " WHERE " + where + " AND " + state + "::text<>$4"
			if _, err = tx.Exec(ctx, query, spec.active, spec.table, restoreID, spec.target); err != nil {
				return fmt.Errorf("preserve restored work provenance %s: %w", spec.table, err)
			}
		}
		supported, err := restoreStateSupported(ctx, tx, spec.table, spec.state, spec.target)
		if err != nil {
			return err
		}
		if !supported {
			unsupported[spec.table] = count
			continue
		}
		// A restored legacy pending refund has no approval. Putting it on hold must
		// not fabricate one merely to satisfy a live execution guard. Writers are
		// stopped; temporarily suspend only that user trigger inside this private TX.
		triggerMode := ""
		if spec.table == "refunds" {
			if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT tgenabled::text FROM pg_trigger WHERE tgrelid='refunds'::regclass AND tgname='refund_approval_guard'),'')`).Scan(&triggerMode); err != nil {
				return err
			}
			if triggerMode != "" && triggerMode != "D" {
				if _, err = tx.Exec(ctx, "ALTER TABLE refunds DISABLE TRIGGER refund_approval_guard"); err != nil {
					return err
				}
			}
		}
		assignments := []string{state + "=$2"}
		if spec.table == "payment_checkout_intents" && columns["restore_hold"] {
			assignments = append(assignments, "restore_hold=true")
		}
		for _, name := range []string{"fence", "lease_fence"} {
			if columns[name] {
				column := pgx.Identifier{name}.Sanitize()
				assignments = append(assignments, column+"="+column+"+1")
			}
		}
		for _, name := range []string{"leased_until", "lease_until", "processing_until", "leased_by", "lease_owner"} {
			if columns[name] {
				assignments = append(assignments, pgx.Identifier{name}.Sanitize()+"=NULL")
			}
		}
		for _, name := range []string{"last_error_code", "result_code", "last_error", "review_reason"} {
			if columns[name] {
				column := pgx.Identifier{name}.Sanitize()
				assignments = append(assignments, column+"=CASE WHEN position('"+restoreReviewCode+"' IN coalesce("+column+",''))>0 THEN "+column+" ELSE concat_ws('; ',nullif("+column+",''),'"+restoreReviewCode+"') END")
				break
			}
		}
		if columns["updated_at"] {
			assignments = append(assignments, "updated_at=statement_timestamp()")
		}
		result, err := tx.Exec(ctx, "UPDATE "+table+" SET "+strings.Join(assignments, ",")+" WHERE "+where, spec.active, spec.target)
		if err != nil {
			return fmt.Errorf("hold restored %s: %w", spec.table, err)
		}
		held[spec.table] = result.RowsAffected()
		if triggerMode != "" && triggerMode != "D" {
			mode := "ENABLE"
			switch triggerMode {
			case "A":
				mode = "ENABLE ALWAYS"
			case "R":
				mode = "ENABLE REPLICA"
			}
			if _, err = tx.Exec(ctx, "ALTER TABLE refunds "+mode+" TRIGGER refund_approval_guard"); err != nil {
				return err
			}
		}
	}
	// Source authentication and one-time operation credentials cannot survive a
	// rewind. Expiration preserves their audit identities without authenticating replay.
	for _, table := range []string{"sessions", "mailbox_sessions", "mailbox_operation_tickets", "registration_requests", "roundcube_bridge_requests"} {
		columns, err := restoreColumns(ctx, tx, table)
		if err != nil {
			return err
		}
		if !columns["expires_at"] {
			continue
		}
		assignments := []string{"expires_at=LEAST(expires_at,statement_timestamp())"}
		if columns["revoked_at"] {
			assignments = append(assignments, "revoked_at=coalesce(revoked_at,statement_timestamp())")
		}
		if _, err = tx.Exec(ctx, "UPDATE "+pgx.Identifier{"public", table}.Sanitize()+" SET "+strings.Join(assignments, ",")); err != nil {
			return err
		}
	}

	// Roundcube keeps the encrypted IMAP credential in its same-PG session
	// table, which has no expires_at. It is not visible to the platform role.
	// The maintenance credential is a member of both fixed owner roles.
	var roundcubeSessions bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='roundcube' AND c.relname='session' AND c.relkind IN('r','p'))`).Scan(&roundcubeSessions); err != nil {
		return err
	}
	if roundcubeSessions {
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE urmail_roundcube"); err != nil {
			return err
		}
		var count int64
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM roundcube.session").Scan(&count); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "TRUNCATE TABLE roundcube.session"); err != nil {
			return fmt.Errorf("invalidate restored Roundcube authentication: %w", err)
		}
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE urmail_platform"); err != nil {
			return err
		}
		held["roundcube.session"] = count
		if count > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO audit_logs(action,target_type,target_id,detail) VALUES('restore.mailbox_sessions_invalidated','restore',$1,jsonb_build_object('count',$2::bigint,'source','roundcube.session'))`, restoreID, count); err != nil {
				return err
			}
		}
	}
	raw, _ := json.Marshal(map[string]any{"restore_id": restoreID, "held": held, "requires_schema_upgrade": unsupported, "reason": restoreReviewCode})
	settings, err := restoreColumns(ctx, tx, "settings")
	if err != nil {
		return err
	}
	if settings["value"] {
		if _, err = tx.Exec(ctx, `INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,'restore_replay_review',$1) ON CONFLICT(scope,scope_id,key) DO UPDATE SET value=EXCLUDED.value,updated_at=statement_timestamp()`, raw); err != nil {
			return err
		}
	}
	alerts, err := restoreColumns(ctx, tx, "operational_alerts")
	if err != nil {
		return err
	}
	if alerts["alert_key"] && len(held) > 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO operational_alerts(alert_key,kind,state) VALUES('restore:external_reconciliation','restore_external_reconciliation','firing') ON CONFLICT(alert_key) DO UPDATE SET state='firing',last_seen_at=statement_timestamp(),resolved_at=NULL`); err != nil {
			return err
		}
	}
	if strict && len(unsupported) > 0 {
		return fmt.Errorf("restored external work cannot yet be held by this schema; keep writers quarantined and complete migrations")
	}
	return nil
}
