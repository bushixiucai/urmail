package datamigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// fakeRecallSource serves synthetic recall_requests rows in place of a PG
// source; rows carry the pgx-style raw values (time.Time for timestamptz,
// []byte for bytea/jsonb) so the same ConvertValue path runs.
type fakeRecallSource struct {
	rows [][]any
	err  error
}

func (s fakeRecallSource) Query(context.Context, string, ...any) (Rows, error) {
	return &fakeRecallRows{rows: s.rows, err: s.err}, nil
}

type fakeRecallRows struct {
	rows [][]any
	err  error
	i    int
	cur  []any
}

func (r *fakeRecallRows) Next() bool {
	if r.i >= len(r.rows) {
		return false
	}
	r.cur = r.rows[r.i]
	r.i++
	return true
}
func (r *fakeRecallRows) Scan(dest ...any) error {
	if len(dest) != len(r.cur) {
		return fmt.Errorf("scan arity %d != %d", len(dest), len(r.cur))
	}
	for i := range dest {
		switch p := dest[i].(type) {
		case *any:
			*p = r.cur[i]
		case *int64:
			v, ok := r.cur[i].(int64)
			if !ok {
				return fmt.Errorf("scan column %d: want int64, got %T", i, r.cur[i])
			}
			*p = v
		case *bool:
			v, ok := r.cur[i].(bool)
			if !ok {
				return fmt.Errorf("scan column %d: want bool, got %T", i, r.cur[i])
			}
			*p = v
		case *string:
			v, ok := r.cur[i].(string)
			if !ok {
				return fmt.Errorf("scan column %d: want string, got %T", i, r.cur[i])
			}
			*p = v
		default:
			return fmt.Errorf("unsupported scan target %T", dest[i])
		}
	}
	return nil
}
func (r *fakeRecallRows) Err() error                 { return r.err }
func (r *fakeRecallRows) Close()                     {}
func (r *fakeRecallRows) Columns() ([]string, error) { return recallColumns, nil }

func recallFixture(t *testing.T) (*sql.DB, TypeMap) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recall-staging.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlitedb.Migrate(ctx, db, os.DirFS("../.."), "migrations_sqlite"); err != nil {
		t.Fatalf("migrate target schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	tm, err := LoadTypeMap("../../migrations_sqlite/type-map.tsv")
	if err != nil {
		t.Fatalf("type map: %v", err)
	}
	seed := []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'b','B')`,
		`INSERT INTO users(id,email,password_hash) VALUES(1,'u1@example.test','x'),(2,'u2@example.test','x')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status,owner_user_id) VALUES(1,1,'T','t','active',1)`,
		`INSERT INTO domains(id,tenant_id,fqdn,verification_token) VALUES(1,1,'a.example.test','v')`,
		`UPDATE domains SET provisioning_state='ready', status='active', verified_at=1 WHERE id=1`,
		`INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash)
		 VALUES(11,1,1,'a','a@a.example.test','x')`,
	}
	for i := 0; i < 12; i++ {
		seed = append(seed, fmt.Sprintf(
			`INSERT INTO archive_entries(id,tenant_id,mailbox_id,message_id,path,received_at,raw_sha256)
			 VALUES(%d,1,11,'m%d','p',1,'r%d')`, 100+i, i, i))
	}
	for _, stmt := range seed {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	return db, tm
}

func snapshotBytes(entry int64, messageID string) []byte {
	return []byte(fmt.Sprintf(
		`{"tenant_id":1,"entry_id":%d,"mailbox_id":11,"message_id":%q,"actor_id":2,"copies":[{"guid":"g1","uid_validity":1,"uid":2}]}`,
		entry, messageID))
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// recallRow builds one raw source tuple in recallColumns order. proof=false
// means no preview fields (initial pending / terminal without proof).
func recallRow(id, entry int64, messageID, status string, epoch int64, proof bool, expires, confirmed, completed any) []any {
	var (
		token, digest any
		snap          any
		selected      = []byte(`[]`)
	)
	if proof {
		snapBytes := snapshotBytes(entry, messageID)
		snap = snapBytes
		token = fmt.Sprintf("tok-%d", id)
		digest = digestOf(snapBytes)
	}
	if status == "pending" && confirmed != nil {
		selected = []byte(`["g1:1:2"]`)
	}
	return []any{
		id, int64(1), messageID, int64(2), status, "reason-" + status,
		time.UnixMicro(1737400000000000 + id*1000).UTC(), entry, int64(11), completed,
		int64(2), token, digest, snap, expires, confirmed, selected, epoch,
		[]byte(`{"items":[]}`), []byte(`[]`),
	}
}

func legalRecallRows() [][]any {
	now := time.Now().UTC()
	future := now.Add(2 * time.Minute)
	past := now.Add(-30 * time.Minute)
	done := now.Add(-10 * time.Minute)
	return [][]any{
		// initial pending: no preview proof, legal in PG, previously refused.
		recallRow(1, 100, "m0", "pending", 0, false, nil, nil, nil),
		// preview at epoch 1 with live proof.
		recallRow(2, 101, "m1", "preview", 1, true, future, nil, nil),
		// preview already expired at a higher epoch: guard would reject INSERT.
		recallRow(3, 102, "m2", "preview", 2, true, past, nil, nil),
		// confirmed pending (proof + confirmed_at + selection).
		recallRow(4, 103, "m3", "pending", 1, true, past, done, nil),
		// local_recalled with confirmation and completion.
		recallRow(5, 104, "m4", "local_recalled", 1, true, past, done, done),
		// terminal without proof (from a never-previewed pending row).
		recallRow(6, 105, "m5", "uncertain", 0, false, nil, nil, nil),
		recallRow(7, 106, "m6", "failed", 3, true, past, nil, done),
		recallRow(8, 107, "m7", "expired", 1, true, past, nil, done),
	}
}

func beginTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	return tx
}

func expectedValues(t *testing.T, tm TypeMap, raw []any) []any {
	t.Helper()
	out := make([]any, len(raw))
	for i, c := range recallColumns {
		v, err := ConvertValue(tm["recall_requests"][c], raw[i])
		if err != nil {
			t.Fatalf("convert %s: %v", c, err)
		}
		out[i] = v
	}
	return out
}

func readAllRecall(t *testing.T, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) [][]any {
	t.Helper()
	rows, err := q.QueryContext(context.Background(),
		"SELECT "+quoteList(recallColumns, false)+" FROM recall_requests ORDER BY id")
	if err != nil {
		t.Fatalf("read recall: %v", err)
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(recallColumns))
		ptrs := make([]any, len(vals))
		for i := range ptrs {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan recall: %v", err)
		}
		out = append(out, vals)
	}
	return out
}

// TestRecallOfflinePreservationKeepsEveryLegalState proves that a single-tx
// staging import preserves every legal recall state byte-for-byte (including
// the columns the replay planner used to drop and states its guard refused),
// restores the suppressed insert guard with an identical DDL hash, and leaves
// all other recall guards in place.
func TestRecallOfflinePreservationKeepsEveryLegalState(t *testing.T) {
	ctx := context.Background()
	db, tm := recallFixture(t)
	before, err := readRecallGuard(ctx, &Target{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	again, err := readRecallGuard(ctx, &Target{DB: db})
	if err != nil || again.Hash != before.Hash {
		t.Fatalf("guard baseline unstable: %v", err)
	}
	triggerNames := func() []string {
		rows, err := db.QueryContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name='recall_requests' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var n []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			n = append(n, s)
		}
		return n
	}
	baselineNames := triggerNames()

	tx := beginTx(t, db)
	tgt := &Target{DB: sqlTxHandle{tx: tx}}
	raw := legalRecallRows()
	stats, refusals, err := migrateRecallTable(ctx, fakeRecallSource{rows: raw}, tgt, tm, TableInfo{Name: "recall_requests"})
	if err != nil {
		tx.Rollback()
		t.Fatalf("preserve: %v", err)
	}
	if len(refusals) != 0 {
		t.Fatalf("legal rows refused: %v", refusals)
	}
	if stats.Rows != len(raw) {
		t.Fatalf("rows=%d want %d", stats.Rows, len(raw))
	}
	staged, err := readRecallGuard(ctx, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if staged.SQL != before.SQL || staged.Hash != before.Hash || staged.Triggers != before.Triggers {
		t.Fatalf("guard drift inside import tx: hash %s -> %s triggers %d -> %d",
			before.Hash, staged.Hash, before.Triggers, staged.Triggers)
	}
	if stats.GuardHash != before.Hash {
		t.Fatalf("stats guard hash %s != baseline %s", stats.GuardHash, before.Hash)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	after, err := readRecallGuard(ctx, &Target{DB: db})
	if err != nil || after.SQL != before.SQL {
		t.Fatalf("guard not restored after commit: %v", err)
	}
	got := triggerNames()
	if strings.Join(got, ",") != strings.Join(baselineNames, ",") {
		t.Fatalf("recall triggers=%v want baseline %v", got, baselineNames)
	}

	stored := readAllRecall(t, db)
	if len(stored) != len(raw) {
		t.Fatalf("stored rows=%d want %d", len(stored), len(raw))
	}
	for i := range raw {
		wantVals := expectedValues(t, tm, raw[i])
		for j, c := range recallColumns {
			if !valuesEqual(wantVals[j], stored[i][j]) {
				t.Fatalf("row %d column %s: stored=%v want=%v", i, c, stored[i][j], wantVals[j])
			}
		}
	}
	var statusRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM recall_requests WHERE status IN ('pending','preview','delivered','failed','expired','uncertain','local_recalled')`).Scan(&statusRows); err != nil || statusRows != len(raw) {
		t.Fatalf("status count=%d err=%v", statusRows, err)
	}
	for s, n := range stats.StatusCounts {
		if n <= 0 {
			t.Fatalf("status %s count=%d", s, n)
		}
	}
	if stats.TableHash == "" || stats.GuardHash == "" {
		t.Fatalf("missing hashes: %+v", stats)
	}
	fk, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer fk.Close()
	if fk.Next() {
		t.Fatalf("foreign_key_check reported a violation")
	}
}

// TestRecallIllegalRowsRejectedAndRetrySafe proves that an internally
// inconsistent row is refused with evidence (never normalized), that a
// failed import leaves zero staged rows and the guard intact, and that a
// clean retry in a fresh transaction succeeds.
func TestRecallIllegalRowsRejectedAndRetrySafe(t *testing.T) {
	ctx := context.Background()
	db, tm := recallFixture(t)
	before, err := readRecallGuard(ctx, &Target{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	legal := legalRecallRows()

	// Digest mismatch: snapshot bytes do not hash to preview_digest.
	bad := recallRow(90, 111, "m11", "preview", 1, true, time.Now().UTC().Add(time.Minute), nil, nil)
	bad[12] = strings.Repeat("0", 64)

	tx := beginTx(t, db)
	stats, refusals, err := migrateRecallTable(ctx, fakeRecallSource{rows: append(append([][]any{}, legal...), bad)}, &Target{DB: sqlTxHandle{tx: tx}}, tm, TableInfo{Name: "recall_requests"})
	if err != nil {
		tx.Rollback()
		t.Fatalf("unexpected hard error: %v", err)
	}
	if len(refusals) != 1 || !strings.HasPrefix(refusals[0].Reason, "RECALL_PREVIEW_DIGEST_INVALID") {
		t.Fatalf("refusals=%v want one RECALL_PREVIEW_DIGEST_INVALID", refusals)
	}
	if stats.Rows != 0 {
		t.Fatalf("refused import inserted %d rows before rejection", stats.Rows)
	}
	if _, err := tx.ExecContext(ctx, `SELECT 1`); err != nil { // tx still usable
		t.Fatal(err)
	}
	tx.Rollback()
	if n := len(readAllRecall(t, db)); n != 0 {
		t.Fatalf("refused import left %d rows", n)
	}

	// FK violation mid-import: entry 999 has no archive_entries parent, so the
	// raw INSERT fails after the guard was dropped; the restore must run and
	// the rollback leaves the target empty and retryable.
	fkBad := recallRow(91, 999, "m0", "pending", 0, false, nil, nil, nil)
	tx = beginTx(t, db)
	_, _, err = migrateRecallTable(ctx, fakeRecallSource{rows: append(append([][]any{}, legal...), fkBad)}, &Target{DB: sqlTxHandle{tx: tx}}, tm, TableInfo{Name: "recall_requests"})
	if err == nil || !strings.Contains(err.Error(), "insert recall") {
		t.Fatalf("FK violation not reported: %v", err)
	}
	tx.Rollback()
	if n := len(readAllRecall(t, db)); n != 0 {
		t.Fatalf("failed import left %d rows", n)
	}
	mid, err := readRecallGuard(ctx, &Target{DB: db})
	if err != nil || mid.SQL != before.SQL {
		t.Fatalf("guard not intact after failed import: %v", err)
	}

	// Safe retry with the clean dataset.
	tx = beginTx(t, db)
	stats, refusals, err = migrateRecallTable(ctx, fakeRecallSource{rows: legal}, &Target{DB: sqlTxHandle{tx: tx}}, tm, TableInfo{Name: "recall_requests"})
	if err != nil || len(refusals) != 0 {
		tx.Rollback()
		t.Fatalf("retry: err=%v refusals=%v", err, refusals)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := len(readAllRecall(t, db)); n != len(legal) {
		t.Fatalf("retry rows=%d want %d", n, len(legal))
	}
	after, err := readRecallGuard(ctx, &Target{DB: db})
	if err != nil || after.SQL != before.SQL || after.Hash != before.Hash {
		t.Fatalf("guard drift after retry: %v", err)
	}
}
