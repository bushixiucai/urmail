package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// B5b follow-up regression: every projection of a NULLable timestamp column
// must read coalesce(col, 0), because the schema keeps those columns as SQL
// NULL (triggers depend on IS NULL) while the Go scan targets are int64 unix
// micros where 0 is the zero value. The gate
// (scripts/sqlite/nullable_timestamp_scan.py --check) proves the wrap exists
// in the generated SQL; this file proves it *behaves*: real SQLite file, real
// generated SQL text, real Scan order and row structs, NULL and non-NULL rows,
// microsecond precision.
//
// Recall rows only exercise the NULL read path here: the 0001 state guards
// require the full preview→pending confirmation flow before confirmed_at may
// become non-NULL, and that flow is covered by the S3 recall-guard probe.
// Physical storage stays NULL-capable everywhere: the raw UPDATEs below only
// touch single rows and never rewrite NULL to 0.

// pgxShim adapts a *sql.DB (modernc SQLite) to the generated pgx-shaped DBTX.
// store.New still requires the pgx surface until the Phase 2 wiring, so the
// shim lets these tests call the real generated methods end to end. Only
// Exec/QueryRow are implemented: single-row statements are all this file
// drives (pgx.Row is just Scan(dest ...any)).
type pgxShim struct{ db *sql.DB }

func (pgxShim) IsSQLite() bool { return true }

func (s pgxShim) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return pgconn.CommandTag{}, err
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (s pgxShim) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return pgxShimRow{row: s.db.QueryRowContext(ctx, query, args...)}
}

func (s pgxShim) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("nullable-timestamp regression drives only single-row statements")
}

type pgxShimRow struct{ row *sql.Row }

func (r pgxShimRow) Scan(dest ...any) error { return r.row.Scan(dest...) }

// newNullTimestampDB opens a real SQLite database with the ported schema and
// fixtures spanning the tables whose nullable timestamps the generated
// statements project: recall_requests, mailboxes, compliance_grants,
// mailbox_import_jobs.
func newNullTimestampDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "nullts.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range []string{"0001_init.sql", "0002_write_side_effects.sql"} {
		body, readErr := readMigration(name)
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if _, execErr := db.ExecContext(ctx, body); execErr != nil {
			t.Fatalf("apply %s: %v", name, execErr)
		}
	}
	const digest = "1111111111111111111111111111111111111111111111111111111111111111"
	for _, stmt := range []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'nullts','NullTS')`,
		`INSERT INTO users(id,email,password_hash,status) VALUES(3,'nullts@example.test','x','active')`,
		// second user as the grantor: the write-side-effect trigger forbids
		// self-authorization (granted_by = user_id).
		`INSERT INTO users(id,email,password_hash,status) VALUES(4,'granter@example.test','x','active')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status,owner_user_id) VALUES(7,1,'NullTS','nullts','active',3)`,
		`INSERT INTO domains(id,tenant_id,fqdn,status,verification_token,verified_at,provisioning_state)
		 VALUES(31,7,'nullts.test','active','tok',1737412800123456,'pending')`,
		`UPDATE domains SET provisioning_state='ready' WHERE id=31`,
		`INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled)
		 VALUES(11,7,31,'nullts','nullts@nullts.test','x',-1,'active',1)`,
		// archive entry the recall points at (FK archive_entries(id, tenant_id)).
		`INSERT INTO archive_entries(id,tenant_id,mailbox_id,message_id,path,received_at)
		 VALUES(101,7,11,'mid-1','cur/x',1737412800123456)`,
		// recall row in the state the guards allow: full preview proof,
		// confirmed_at NULL. The state machine itself is the S3 probe's job.
		`INSERT INTO recall_requests(tenant_id,archive_entry_id,mailbox_id,message_id,requested_by,status,
		   preview_token,preview_digest,preview_snapshot,preview_expires_at,attempt_epoch)
		 VALUES(7,101,11,'mid-1',3,'preview','tok-1','digest-1',
		   '{"tenant_id":7,"entry_id":101,"mailbox_id":11,"message_id":"mid-1","copies":[{"guid":"g1","uid_validity":1,"uid":5,"message_guid":"abc123"}]}',
		   (CAST(strftime('%s','now') AS INTEGER)*1000000)+60000000, 1)`,
		// grant with owner_confirmed_at set; expires_at / revoked_at stay NULL.
		`INSERT INTO compliance_grants(id,tenant_id,user_id,scope,reason,granted_by,owner_confirmed_at)
		 VALUES(201,7,3,'metadata_only','incident review',4,1700000000000001)`,
		// import job leaves confirmed_at / lease_until NULL by default.
		`INSERT INTO mailbox_import_jobs(id,tenant_id,mailbox_id,request_key,format,filename,folder,
		 uid_validity,source_digest,preview_digest,duplicate_policy,source_bytes)
		 VALUES(301,7,11,'nullts-job-0001','eml','a.eml','INBOX',1,'` + digest + `','` + digest + `','skip',100)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return db, ctx
}

func TestNullableTimestampScanViaGeneratedQueries(t *testing.T) {
	db, ctx := newNullTimestampDB(t)
	q := New(pgxShim{db})

	const micro1 = int64(1700000000000001) // trailing 1 micro, catches truncation

	// INSERT..RETURNING: the new import job has NULL confirmed_at /
	// lease_until; the generated RETURNING must hand the int64 scan targets
	// 0, while preview_expires_at (NOT NULL DEFAULT now+900s) stays real.
	job, err := q.CreateMailboxImportJob(ctx, CreateMailboxImportJobParams{
		TenantID:        7,
		MailboxID:       11,
		RequestKey:      "nullts-job-0002",
		Format:          "eml",
		Filename:        "b.eml",
		Folder:          "INBOX",
		UidValidity:     1,
		SourceDigest:    "2222222222222222222222222222222222222222222222222222222222222222",
		PreviewDigest:   "2222222222222222222222222222222222222222222222222222222222222222",
		DuplicatePolicy: "skip",
		SourceBytes:     100,
	})
	if err != nil {
		t.Fatalf("CreateMailboxImportJob: %v", err)
	}
	if job.ConfirmedAt != 0 || job.LeaseUntil != 0 {
		t.Fatalf("CreateMailboxImportJob: NULL timestamps must scan as 0, got confirmed=%d lease=%d",
			job.ConfirmedAt, job.LeaseUntil)
	}
	if job.PreviewExpiresAt <= 0 {
		t.Fatalf("CreateMailboxImportJob: non-NULL preview_expires_at must be positive, got %d", job.PreviewExpiresAt)
	}

	// recall: confirmed_at NULL reads as 0 through the read path, and the
	// non-NULL preview_expires_at round-trips bit for bit (raw value read
	// beside the method call — the clock value is not predicted).
	rec, err := q.GetRecallByID(ctx, GetRecallByIDParams{TenantID: 7, ID: 1})
	if err != nil {
		t.Fatalf("GetRecallByID: %v", err)
	}
	if rec.ConfirmedAt != 0 {
		t.Fatalf("GetRecallByID: NULL confirmed_at must read 0, got %d", rec.ConfirmedAt)
	}
	var rawPreviewExpires int64
	if err := db.QueryRowContext(ctx,
		`SELECT preview_expires_at FROM recall_requests WHERE id=1`).Scan(&rawPreviewExpires); err != nil {
		t.Fatalf("raw preview_expires_at: %v", err)
	}
	if rec.PreviewExpiresAt != rawPreviewExpires {
		t.Fatalf("GetRecallByID: preview_expires_at must round-trip %d exactly, got %d",
			rawPreviewExpires, rec.PreviewExpiresAt)
	}

	// mailboxes: the insert-side usage defaults trigger fills usage_measured_at
	// with a real timestamp, so this path must preserve the non-NULL value.
	mb, err := q.GetMailboxByID(ctx, GetMailboxByIDParams{TenantID: 7, ID: 11})
	if err != nil {
		t.Fatalf("GetMailboxByID: %v", err)
	}
	if mb.UsageMeasuredAt <= 0 {
		t.Fatalf("GetMailboxByID: usage_measured_at trigger value must be positive, got %d", mb.UsageMeasuredAt)
	}

	// compliance grant: one statement covering both shapes —
	// owner_confirmed_at is set (exact micros), expires_at/revoked_at NULL.
	g, err := q.GetComplianceGrant(ctx, GetComplianceGrantParams{TenantID: 7, UserID: 3, RequiredScope: "metadata_only"})
	if err != nil {
		t.Fatalf("GetComplianceGrant: %v", err)
	}
	if g.ExpiresAt != 0 || g.RevokedAt != 0 {
		t.Fatalf("GetComplianceGrant: NULL expires_at/revoked_at must read 0, got %d/%d", g.ExpiresAt, g.RevokedAt)
	}
	if g.OwnerConfirmedAt != micro1 {
		t.Fatalf("GetComplianceGrant: owner_confirmed_at must round-trip %d, got %d", micro1, g.OwnerConfirmedAt)
	}

	// import job: INSERT..RETURNING and SELECT both cover NULL timestamps.
	// Then set a valid past lease time with sub-second precision and require
	// the generated SELECT to return every microsecond unchanged.
	j, err := q.GetMailboxImportJob(ctx, GetMailboxImportJobParams{TenantID: 7, MailboxID: 11, ID: 301})
	if err != nil {
		t.Fatalf("GetMailboxImportJob (NULL): %v", err)
	}
	if j.ConfirmedAt != 0 || j.LeaseUntil != 0 {
		t.Fatalf("GetMailboxImportJob: NULL confirmed_at/lease_until must read 0, got %d/%d", j.ConfirmedAt, j.LeaseUntil)
	}
	if _, err := db.ExecContext(ctx, `UPDATE mailbox_import_jobs SET lease_until=? WHERE id=301`, micro1); err != nil {
		t.Fatalf("seed lease_until: %v", err)
	}
	j, err = q.GetMailboxImportJob(ctx, GetMailboxImportJobParams{TenantID: 7, MailboxID: 11, ID: 301})
	if err != nil {
		t.Fatalf("GetMailboxImportJob (non-NULL): %v", err)
	}
	if j.LeaseUntil != micro1 {
		t.Fatalf("GetMailboxImportJob: lease_until must round-trip %d, got %d", micro1, j.LeaseUntil)
	}
}

// TestNullableTimestampBareScanFailsOnSQLite pins the hazard the coalesce wrap
// exists for: on this driver, scanning SQL NULL into a bare int64 is an error.
// If this ever stops failing, the wrap is dead weight and the gate is lying
// about the failure mode it prevents.
func TestNullableTimestampBareScanFailsOnSQLite(t *testing.T) {
	db, ctx := newNullTimestampDB(t)
	var bare int64
	err := db.QueryRowContext(ctx,
		`SELECT confirmed_at FROM recall_requests WHERE tenant_id=7 AND archive_entry_id=101`).Scan(&bare)
	if err == nil {
		t.Fatal("bare NULL scan into int64 succeeded; the documented failure mode is gone")
	}
	if !strings.Contains(err.Error(), "NULL") {
		t.Fatalf("unexpected bare-scan error: %v", err)
	}
}
