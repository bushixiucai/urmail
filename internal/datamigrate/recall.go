package datamigrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Recall row preservation: the recall_requests lifecycle differs between
// engines, so a lifecycle replay cannot represent every legal PG state
// (initial pending has no preview proof; expired previews are rejected by
// the SQLite write guard). The offline importer therefore copies every legal
// source row verbatim — all columns, PK, status, attempt_epoch, timestamps
// and preview proof bytes — into the private staging database, inside one
// transaction that temporarily suppresses ONLY recall_state_guard_insert.
// Every other constraint (CHECK, PK, UNIQUE, FK, recall_state_guard_update,
// recall_message_identity_guard__insert) stays active. The suppressed guard
// is restored verbatim from sqlite_master and its DDL hash re-verified before
// the import transaction can commit; any failure rolls the staging file back
// and the source is never written. Rows that are internally inconsistent
// (illegal under any engine) are refused with per-row evidence and block the
// import — never silently skipped, never normalized.

// RecallRefusal is one source row that is illegal and therefore rejected.
type RecallRefusal struct {
	PK     int64
	Status string
	Epoch  int64
	Reason string
}

// RecallImportStats reports the staged recall preservation.
type RecallImportStats struct {
	Rows         int
	StatusCounts map[string]int64
	TableHash    string
	GuardHash    string
}

// recallColumns is the full recall_requests column set in schema order. All
// columns are copied; no target default may stand in for source data.
var recallColumns = []string{
	"id", "tenant_id", "message_id", "requested_by", "status", "reason",
	"created_at", "archive_entry_id", "mailbox_id", "completed_at",
	"preview_actor_id", "preview_token", "preview_digest", "preview_snapshot",
	"preview_expires_at", "confirmed_at", "selected_copy_keys", "attempt_epoch",
	"local_result", "result_history",
}

var recallStatuses = map[string]bool{
	"pending": true, "preview": true, "delivered": true, "failed": true,
	"expired": true, "uncertain": true, "local_recalled": true,
}

const recallInsertGuard = "recall_state_guard_insert"

type recallGuardState struct {
	SQL      string
	Hash     string
	Triggers int64
}

func readRecallGuard(ctx context.Context, t *Target) (recallGuardState, error) {
	var st recallGuardState
	var sqlText string
	err := t.DB.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`,
		recallInsertGuard,
	).Scan(&sqlText)
	if err != nil {
		return st, fmt.Errorf("read %s: %w", recallInsertGuard, err)
	}
	if err := t.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='recall_requests'`,
	).Scan(&st.Triggers); err != nil {
		return st, fmt.Errorf("count recall triggers: %w", err)
	}
	st.SQL = sqlText
	st.Hash = sha256Hex([]byte(sqlText))
	return st, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// migrateRecallTable copies recall_requests rows verbatim into the target
// (caller-owned transaction). It returns refusal evidence for illegal rows;
// when any row is refused the caller blocks the import (transaction rolls
// back, no done marker).
func migrateRecallTable(ctx context.Context, src Source, t *Target, tm TypeMap, ti TableInfo) (RecallImportStats, []RecallRefusal, error) {
	stats := RecallImportStats{StatusCounts: map[string]int64{}}
	cm, ok := tm["recall_requests"]
	if !ok {
		return stats, nil, fmt.Errorf("recall_requests missing from type map")
	}
	for _, c := range recallColumns {
		if _, ok := cm[c]; !ok {
			return stats, nil, fmt.Errorf("recall column %s missing from type map", c)
		}
	}

	// Phase 1: read and validate every source row before touching any DDL.
	rows, err := src.Query(ctx, "SELECT "+quoteList(recallColumns, false)+" FROM recall_requests ORDER BY id")
	if err != nil {
		return stats, nil, fmt.Errorf("scan: %w", err)
	}
	var (
		converted [][]any
		refusals  []RecallRefusal
		hashLines []string
	)
	for rows.Next() {
		raw := make([]any, len(recallColumns))
		ptrs := make([]any, len(raw))
		for i := range ptrs {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return stats, nil, fmt.Errorf("scan: %w", err)
		}
		vals := make([]any, len(recallColumns))
		for i, c := range recallColumns {
			v, err := ConvertValue(cm[c], raw[i])
			if err != nil {
				rows.Close()
				return stats, nil, fmt.Errorf("column %s: %w", c, err)
			}
			vals[i] = v
		}
		refusal, err := validateRecallRow(vals)
		if err != nil {
			rows.Close()
			return stats, nil, err
		}
		if refusal != nil {
			refusals = append(refusals, *refusal)
			continue
		}
		line, err := canonicalJSON(recallColumns, vals)
		if err != nil {
			rows.Close()
			return stats, nil, fmt.Errorf("recall row id=%v canonical: %w", vals[0], err)
		}
		hashLines = append(hashLines, line)
		converted = append(converted, vals)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return stats, nil, err
	}
	rows.Close()
	if len(refusals) > 0 {
		// Illegal rows are rejected with evidence; the caller blocks the
		// import so the staging file can never publish a partial dataset.
		return stats, refusals, nil
	}

	// Phase 2: suppress ONLY the insert guard, copy raw rows, restore it.
	baseline, err := readRecallGuard(ctx, t)
	if err != nil {
		return stats, nil, err
	}
	if _, err := t.DB.ExecContext(ctx, `DROP TRIGGER `+recallInsertGuard); err != nil {
		return stats, nil, fmt.Errorf("drop %s: %w", recallInsertGuard, err)
	}
	insertSQL := "INSERT INTO recall_requests(" + quoteList(recallColumns, false) + ") VALUES(" +
		placeholders(len(recallColumns)) + ")"
	var copyErr error
	for _, vals := range converted {
		if _, err := t.DB.ExecContext(ctx, insertSQL, vals...); err != nil {
			copyErr = fmt.Errorf("insert recall id=%v: %w", vals[0], err)
			break
		}
		stats.Rows++
		stats.StatusCounts[statusOf(vals)]++
	}
	// Restore verbatim from the captured DDL regardless of the copy outcome.
	if _, err := t.DB.ExecContext(ctx, baseline.SQL); err != nil {
		return stats, nil, fmt.Errorf("restore %s: %w (copy error: %w)", recallInsertGuard, err, copyErr)
	}
	if copyErr != nil {
		return stats, nil, copyErr
	}
	restored, err := readRecallGuard(ctx, t)
	if err != nil {
		return stats, nil, err
	}
	if restored.SQL != baseline.SQL || restored.Hash != baseline.Hash {
		return stats, nil, fmt.Errorf("guard %s DDL drift after restore (baseline %s, restored %s)",
			recallInsertGuard, baseline.Hash, restored.Hash)
	}
	if restored.Triggers != baseline.Triggers {
		return stats, nil, fmt.Errorf("recall trigger count drift after restore: %d -> %d", baseline.Triggers, restored.Triggers)
	}
	stats.GuardHash = restored.Hash

	// Phase 3: read back the staged rows and prove counts + full-table hash.
	stats.TableHash = sha256Hex([]byte(strings.Join(hashLines, "\n")))
	if err := verifyRecallStaging(ctx, t, stats, hashLines); err != nil {
		return stats, nil, err
	}
	return stats, nil, nil
}

func placeholders(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
	}
	return b.String()
}

func statusOf(vals []any) string {
	if s, ok := vals[4].(string); ok {
		return s
	}
	if b, ok := vals[4].([]byte); ok {
		return string(b)
	}
	return fmt.Sprintf("%v", vals[4])
}

// validateRecallRow rejects rows that are internally inconsistent (illegal
// in either engine). All legal states — initial pending without proof,
// expired preview, any attempt_epoch, terminals — are preserved as-is.
func validateRecallRow(vals []any) (*RecallRefusal, error) {
	get := func(i int) any { return vals[i] }
	id, _ := toInt64(get(0))
	status := statusOf(vals)
	epoch, _ := toInt64(get(17))
	refuse := func(reason string) (*RecallRefusal, error) {
		return &RecallRefusal{PK: id, Status: status, Epoch: epoch, Reason: reason}, nil
	}
	if !recallStatuses[status] {
		return refuse(fmt.Sprintf("RECALL_INVALID_STATUS: unknown status %q", status))
	}
	if epoch < 0 {
		return refuse("RECALL_INVALID_EPOCH: attempt_epoch < 0")
	}
	token, digest, snap := get(11), get(12), get(13)
	expires := get(14)
	confirmed := get(15)
	proofParts := countPresent(token, digest, snap, expires)
	if proofParts != 0 && proofParts != 4 {
		return refuse("RECALL_PROOF_PARTIAL: preview proof fields must be all present or all absent")
	}
	if proofParts == 4 {
		digestHex, ok := asString(digest)
		if !ok || !isSHA256Hex(digestHex) {
			return refuse("RECALL_PREVIEW_DIGEST_INVALID: preview_digest is not a sha256 hex value")
		}
		snapshot, ok := asBytes(snap)
		if !ok {
			return refuse("RECALL_PREVIEW_SNAPSHOT_INVALID: preview_snapshot is not bytes")
		}
		if !strings.EqualFold(sha256Hex(snapshot), digestHex) {
			return refuse("RECALL_PREVIEW_DIGEST_INVALID: sha256(preview_snapshot) != preview_digest")
		}
		var obj map[string]any
		dec := json.NewDecoder(strings.NewReader(string(snapshot)))
		dec.UseNumber()
		if err := dec.Decode(&obj); err != nil {
			return refuse("RECALL_PREVIEW_SNAPSHOT_INVALID: preview_snapshot is not a JSON object")
		}
		for k, idx := range map[string]int{"tenant_id": 1, "entry_id": 7, "mailbox_id": 8} {
			got, ok := numberText(obj[k])
			want, _ := toInt64(get(idx))
			if !ok || got != fmt.Sprintf("%d", want) {
				return refuse(fmt.Sprintf("RECALL_IDENTITY_MISMATCH: snapshot.%s does not match the row", k))
			}
		}
		if got, ok := obj["message_id"].(string); !ok || got != messageIDOf(vals[2]) {
			return refuse("RECALL_IDENTITY_MISMATCH: snapshot.message_id does not match the row")
		}
		if _, ok := obj["copies"].([]any); !ok {
			return refuse("RECALL_IDENTITY_MISMATCH: snapshot.copies is not an array")
		}
	}
	switch status {
	case "preview":
		if proofParts != 4 {
			return refuse("RECALL_PREVIEW_PROOF_REQUIRED: status=preview without complete preview proof")
		}
		if confirmed != nil {
			return refuse("RECALL_INVALID_STATE: status=preview with confirmed_at set")
		}
	case "local_recalled":
		if confirmed == nil {
			return refuse("RECALL_VERIFIED_LOCAL_PROOF_REQUIRED: local_recalled without confirmed_at")
		}
		if _, ok := asString(token); !ok {
			return refuse("RECALL_VERIFIED_LOCAL_PROOF_REQUIRED: local_recalled without preview_token")
		}
	}
	return nil, nil
}

func countPresent(vs ...any) int {
	n := 0
	for _, v := range vs {
		if present(v) {
			n++
		}
	}
	return n
}

func present(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case []byte:
		return len(x) > 0
	case string:
		return x != ""
	}
	return true
}

func asString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case []byte:
		return string(x), true
	}
	return "", false
}

func asBytes(v any) ([]byte, bool) {
	switch x := v.(type) {
	case []byte:
		return x, true
	case string:
		return []byte(x), true
	}
	return nil, false
}

func messageIDOf(v any) string {
	s, _ := asString(v)
	return s
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func numberText(v any) (string, bool) {
	switch x := v.(type) {
	case json.Number:
		return x.String(), true
	case float64:
		return fmt.Sprintf("%d", int64(x)), true
	case string:
		return x, true
	}
	return "", false
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	case string:
		var n int64
		if _, err := fmt.Sscan(x, &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

// verifyRecallStaging reads the staged rows back and proves the row count,
// per-status counts and full-table hash match the source-side computation.
func verifyRecallStaging(ctx context.Context, t *Target, stats RecallImportStats, expected []string) error {
	rows, err := t.DB.QueryContext(ctx,
		"SELECT "+quoteList(recallColumns, false)+" FROM recall_requests ORDER BY id")
	if err != nil {
		return fmt.Errorf("staging readback: %w", err)
	}
	defer rows.Close()
	var (
		n        int
		counts   = map[string]int64{}
		hashList []string
	)
	for rows.Next() {
		vals := make([]any, len(recallColumns))
		ptrs := make([]any, len(vals))
		for i := range ptrs {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("staging scan: %w", err)
		}
		line, err := canonicalJSON(recallColumns, vals)
		if err != nil {
			return fmt.Errorf("staging canonical: %w", err)
		}
		hashList = append(hashList, line)
		counts[statusOf(vals)]++
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	hash := sha256Hex([]byte(strings.Join(hashList, "\n")))
	if n != stats.Rows {
		return fmt.Errorf("staging row count %d != source %d", n, stats.Rows)
	}
	if hash != stats.TableHash {
		if len(expected) == len(hashList) {
			for i := range expected {
				if expected[i] != hashList[i] {
					return fmt.Errorf("staging row %d drifted:\n source=%s\n staged=%s", i, expected[i], hashList[i])
				}
			}
		}
		return fmt.Errorf("staging table hash %s != source %s", hash, stats.TableHash)
	}
	if len(counts) != len(stats.StatusCounts) {
		return fmt.Errorf("staging status set drift: %v != %v", counts, stats.StatusCounts)
	}
	for s, c := range stats.StatusCounts {
		if counts[s] != c {
			return fmt.Errorf("staging status %s count %d != source %d", s, counts[s], c)
		}
	}
	return nil
}
