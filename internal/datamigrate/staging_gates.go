package datamigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Supported PG source schema range for the controlled recall staging import.
// The staging copy is generated against this frozen chain; an older or
// unknown version is refused before any file is created.
const (
	S5SupportedPGSchemaMin = 159
	S5SupportedPGSchemaMax = 160
)

// verifySourceSchema refuses dirty, older or unknown source schemas before
// any staging write. It reads the golang-migrate bookkeeping table only.
func verifySourceSchema(ctx context.Context, src Source) error {
	rows, err := src.Query(ctx, "SELECT version, dirty FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("read source schema version: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read source schema version: %w", err)
		}
		return fmt.Errorf("source schema version is unknown (no schema_migrations row); supported range %d-%d",
			S5SupportedPGSchemaMin, S5SupportedPGSchemaMax)
	}
	var version int64
	var dirty bool
	if err := rows.Scan(&version, &dirty); err != nil {
		return fmt.Errorf("scan source schema version: %w", err)
	}
	if dirty {
		return fmt.Errorf("source schema version %d is dirty; refusing staging import", version)
	}
	if version < S5SupportedPGSchemaMin || version > S5SupportedPGSchemaMax {
		return fmt.Errorf("source schema version %d is outside the supported range %d-%d",
			version, S5SupportedPGSchemaMin, S5SupportedPGSchemaMax)
	}
	return nil
}

// verifyReadSnapshot proves the import reads from one read-only REPEATABLE
// READ transaction; every catalog, copy and verification statement shares it,
// so concurrent commits cannot mix generations and PostgreSQL refuses writes.
func verifyReadSnapshot(ctx context.Context, src Source, report *StagingReport) error {
	var isolation, readOnly string
	rows, err := src.Query(ctx, "SELECT current_setting('transaction_isolation'), current_setting('transaction_read_only')")
	if err != nil {
		return fmt.Errorf("read snapshot settings: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return errors.New("read snapshot settings returned no row")
	}
	if err := rows.Scan(&isolation, &readOnly); err != nil {
		return fmt.Errorf("scan snapshot settings: %w", err)
	}
	if isolation != "repeatable read" {
		return fmt.Errorf("source reads are not in a repeatable read snapshot (isolation=%s)", isolation)
	}
	if readOnly != "on" {
		return fmt.Errorf("source snapshot is not read-only (transaction_read_only=%s)", readOnly)
	}
	report.SnapshotIsolation = isolation
	report.SnapshotReadOnly = readOnly
	return nil
}

// sourceRecallFingerprint hashes the recall rows plus the schema version so
// the import can prove the source was not written to (before vs after).
func sourceRecallFingerprint(ctx context.Context, src Source) (string, error) {
	rows, err := src.Query(ctx, "SELECT "+quoteList(recallColumns, false)+" FROM recall_requests ORDER BY id")
	if err != nil {
		return "", fmt.Errorf("source fingerprint: %w", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		raw := make([]any, len(recallColumns))
		ptrs := make([]any, len(raw))
		for i := range ptrs {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", fmt.Errorf("source fingerprint scan: %w", err)
		}
		line, err := canonicalJSON(recallColumns, raw)
		if err != nil {
			return "", fmt.Errorf("source fingerprint canonical: %w", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	var version int64
	var dirty bool
	vrows, err := src.Query(ctx, "SELECT version, dirty FROM schema_migrations")
	if err != nil {
		return "", fmt.Errorf("source fingerprint version: %w", err)
	}
	defer vrows.Close()
	if vrows.Next() {
		if err := vrows.Scan(&version, &dirty); err != nil {
			return "", fmt.Errorf("source fingerprint version scan: %w", err)
		}
	}
	payload := fmt.Sprintf("schema=%d;dirty=%t;rows=%d;hash=%s", version, dirty, len(lines),
		sha256Hex([]byte(strings.Join(lines, "\n"))))
	return sha256Hex([]byte(payload)), nil
}

// verifySourceUnchanged compares the source recall ledger with its pre-import
// fingerprint; any drift blocks publish.
func verifySourceUnchanged(ctx context.Context, src Source, before string) error {
	after, err := sourceRecallFingerprint(ctx, src)
	if err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("source recall ledger changed during the import (before %s, after %s); publish blocked", before, after)
	}
	return nil
}

// RuntimeGateReport is the outcome of executing the restored recall
// constraints on the staged database.
type RuntimeGateReport struct {
	Checked int
	Skipped string
}

// runRuntimeRecallGates executes positive and negative recall writes against
// the restored guard in one transaction that is always rolled back, so the
// staged file keeps exactly the preserved rows. It proves the guard is
// functional (not merely present) before publish: legal preview and
// confirmation are accepted; direct pending insert, cross-tenant preview,
// identity change, wrong epoch and unproven local recall are rejected.
func runRuntimeRecallGates(ctx context.Context, db *sql.DB) (RuntimeGateReport, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return RuntimeGateReport{}, fmt.Errorf("runtime gates begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var tenantID, mailboxID int64
	if err := tx.QueryRowContext(ctx,
		"SELECT tenant_id, mailbox_id FROM archive_entries WHERE mailbox_id IS NOT NULL ORDER BY id LIMIT 1",
	).Scan(&tenantID, &mailboxID); err != nil {
		if err == sql.ErrNoRows {
			return RuntimeGateReport{Skipped: "no archive entry in staged source"}, nil
		}
		return RuntimeGateReport{}, fmt.Errorf("runtime gates parent lookup: %w", err)
	}
	var actorID int64
	if err := tx.QueryRowContext(ctx,
		"SELECT id FROM users ORDER BY id LIMIT 1",
	).Scan(&actorID); err != nil {
		return RuntimeGateReport{}, fmt.Errorf("runtime gates actor lookup: %w", err)
	}
	before, err := guardFingerprint(ctx, tx)
	if err != nil {
		return RuntimeGateReport{}, err
	}
	countBefore, err := recallCount(ctx, tx)
	if err != nil {
		return RuntimeGateReport{}, err
	}

	makeEntry := func(tag string) (int64, string, error) {
		messageID := fmt.Sprintf("gate-%s-%d@runtime.test", tag, time.Now().UnixNano())
		var entryID int64
		err := tx.QueryRowContext(ctx,
			`INSERT INTO archive_entries(tenant_id, mailbox_id, message_id, path, received_at, raw_sha256)
			 VALUES(?,?,?,?,?,?) RETURNING id`,
			tenantID, mailboxID, messageID, "gate/runtime", time.Now().UTC().UnixMicro(),
			fmt.Sprintf("gate-%s-%d", tag, time.Now().UnixNano()),
		).Scan(&entryID)
		return entryID, messageID, err
	}
	expires := time.Now().UTC().Add(60 * time.Second).UnixMicro()
	insertPreview := func(tenant, epoch, id, entryID int64, messageID string) error {
		snap := []byte(fmt.Sprintf(
			`{"tenant_id":%d,"entry_id":%d,"mailbox_id":%d,"message_id":%q,"actor_id":%d,"copies":[{"guid":"gate","uid_validity":1,"uid":1,"message_guid":"gate"}]}`,
			tenant, entryID, mailboxID, messageID, actorID))
		_, err := tx.ExecContext(ctx, `INSERT INTO recall_requests
			(id, tenant_id, message_id, requested_by, status, reason, created_at,
			 archive_entry_id, mailbox_id, preview_actor_id, preview_token, preview_digest,
			 preview_snapshot, preview_expires_at, attempt_epoch)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, tenant, messageID, actorID, "preview", "runtime gate", time.Now().UTC().UnixMicro(),
			entryID, mailboxID, actorID, "runtime-gate", sha256Hex(snap), snap, expires, epoch)
		return err
	}
	entryID, messageID, err := makeEntry("primary")
	if err != nil {
		return RuntimeGateReport{}, fmt.Errorf("runtime gates entry: %w", err)
	}
	checked := 0
	if err := insertPreview(tenantID, 1, 900000001, entryID, messageID); err != nil {
		return RuntimeGateReport{}, fmt.Errorf("runtime gate positive preview rejected: %w", err)
	}
	checked++
	if _, err := tx.ExecContext(ctx, `UPDATE recall_requests
		SET status='pending', confirmed_at=?, selected_copy_keys='["gate:1:1"]'
		WHERE id=?`, time.Now().UTC().UnixMicro(), 900000001); err != nil {
		return RuntimeGateReport{}, fmt.Errorf("runtime gate positive confirmation rejected: %w", err)
	}
	checked++

	negatives := []struct {
		name string
		want string
		fn   func() error
	}{
		{"direct pending insert", "RECALL_PREVIEW_REQUIRED", func() error {
			_, err := tx.ExecContext(ctx, `INSERT INTO recall_requests
				(tenant_id, message_id, requested_by, status, archive_entry_id, mailbox_id)
				VALUES(?,?,?, 'pending', ?,?)`, tenantID, messageID, actorID, entryID, mailboxID)
			return err
		}},
		{"cross-tenant preview", "RECALL_PREVIEW_TARGET_MISMATCH", func() error {
			return insertPreview(tenantID+1, 1, 900000002, entryID, messageID)
		}},
		{"identity change", "RECALL_IDENTITY_IMMUTABLE", func() error {
			_, err := tx.ExecContext(ctx, "UPDATE recall_requests SET tenant_id=? WHERE id=?", tenantID+1, 900000001)
			return err
		}},
		{"epoch not advanced", "RECALL_PREVIEW_PROOF_REQUIRED", func() error {
			secondEntry, secondMessage, err := makeEntry("epoch")
			if err != nil {
				return fmt.Errorf("setup second preview entry: %w", err)
			}
			if err := insertPreview(tenantID, 1, 900000003, secondEntry, secondMessage); err != nil {
				return fmt.Errorf("setup second preview: %w", err)
			}
			_, err = tx.ExecContext(ctx, `UPDATE recall_requests SET preview_token='runtime-gate-2',
				preview_expires_at=? WHERE id=?`, expires, 900000003)
			return err
		}},
		{"unproven local recall", "RECALL_VERIFIED_LOCAL_PROOF_REQUIRED", func() error {
			_, err := tx.ExecContext(ctx, "UPDATE recall_requests SET status='local_recalled', completed_at=NULL WHERE id=?",
				900000001)
			return err
		}},
	}
	for _, neg := range negatives {
		err := neg.fn()
		if err == nil {
			return RuntimeGateReport{}, fmt.Errorf("runtime gate negative %q was accepted", neg.name)
		}
		if !strings.Contains(err.Error(), neg.want) {
			return RuntimeGateReport{}, fmt.Errorf("runtime gate negative %q rejected with %w, want %s", neg.name, err, neg.want)
		}
		checked++
	}

	after, err := guardFingerprint(ctx, tx)
	if err != nil {
		return RuntimeGateReport{}, err
	}
	if after != before {
		return RuntimeGateReport{}, fmt.Errorf("runtime gates changed the recall guard (before %s, after %s)", before, after)
	}
	_ = tx.Rollback()
	countAfter, err := recallCount(ctx, db)
	if err != nil {
		return RuntimeGateReport{}, err
	}
	if countAfter != countBefore {
		return RuntimeGateReport{}, fmt.Errorf("runtime gates leaked rows (%d -> %d)", countBefore, countAfter)
	}
	return RuntimeGateReport{Checked: checked}, nil
}

func guardFingerprint(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT name || ':' || coalesce(sql,'') FROM sqlite_master WHERE type='trigger' AND tbl_name='recall_requests' ORDER BY name`)
	if err != nil {
		return "", fmt.Errorf("guard fingerprint: %w", err)
	}
	defer func() { _ = rows.Close() }() // read-only rows
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", err
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return sha256Hex([]byte(b.String())), nil
}

func recallCount(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int64, error) {
	var n int64
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM recall_requests").Scan(&n); err != nil {
		return 0, fmt.Errorf("recall count: %w", err)
	}
	return n, nil
}
