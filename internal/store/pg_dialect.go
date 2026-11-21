package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	pgstore "github.com/bushixiucai/urmail/internal/store/generated/postgres"
)

var ErrPostgreSQLQueryUnavailable = errors.New("store: PostgreSQL query variant unavailable")

var sqliteOnlySQL = regexp.MustCompile(`(?i)(\?[0-9]+|\bstrftime\s*\(|\bjson_each\s*\(|\bjson_object\s*\()`)

// pgExtraStatements carries native PostgreSQL SQL for methods whose shared
// generated statement is SQLite-only (S4/B4 handwritten splits with no PG
// sqlc twin). Values were derived from the origin/main PostgreSQL query set;
// lookup happens only when the generated PG manifest misses the method name.
var pgExtraStatements = map[string]string{
	"ArchiveCasesGetPendingApproval": `-- name: ArchiveCasesGetPendingApproval :one
SELECT r.id FROM archive_approval_requests r
WHERE r.tenant_id=$1 AND r.investigation_id=$2
 AND r.requester_user_id=$3 AND r.source_digest=$4 AND r.state='pending'
 AND r.expires_at>now()
ORDER BY r.id LIMIT 1
`,
	"ArchiveCasesInsertApproval": `-- name: ArchiveCasesInsertApproval :one
INSERT INTO archive_approval_requests(tenant_id, investigation_id, requester_user_id, action, source_digest, expires_at, case_version, source_snapshot)
SELECT $1, i.id, $2, 'export', $3,
 LEAST(i.expires_at, now()), i.version, $4
FROM archive_investigations i
WHERE i.tenant_id=$1 AND i.id=$5 AND i.status='open' AND i.version=$6
RETURNING id
`,
	"GetRecallByEntry": `-- name: GetRecallByEntry :one
SELECT id, tenant_id, message_id, requested_by, status, reason, created_at, archive_entry_id, mailbox_id, completed_at, preview_actor_id, preview_token, preview_digest, preview_snapshot, preview_expires_at, confirmed_at, selected_copy_keys, attempt_epoch, local_result, result_history FROM recall_requests r WHERE r.tenant_id=$1 AND r.archive_entry_id=$2
`,
	"GetRecallReviewPreview": `-- name: GetRecallReviewPreview :one
SELECT id, tenant_id, message_id, requested_by, status, reason, created_at, archive_entry_id, mailbox_id, completed_at, preview_actor_id, preview_token, preview_digest, preview_snapshot, preview_expires_at, confirmed_at, selected_copy_keys, attempt_epoch, local_result, result_history FROM recall_requests r WHERE r.tenant_id=$1 AND r.id=$2
 AND r.preview_actor_id=$3 AND r.preview_token=$4 AND r.preview_digest=$5
`,
}

// Parameter positions whose PostgreSQL generated signature takes a native
// timestamptz while the SQLite generated signature stores unix microseconds.
// This explicit bridge is B2 adapter metadata, not a SQL text rewrite.
// Parameter positions whose PostgreSQL generated signature takes a native
// timestamptz/timestamp while the shared (SQLite-era) signature stores unix
// microseconds. Derived systematically by diffing every PG twin Params struct
// against its shared counterpart (scripts/sqlite generated pairings); adapted
// at the B2 dialect seam, never by rewriting SQL text.
var postgresTimestampArgIndexes = map[string][]int{
	"ArchiveRetentionCandidateDigest":        {1},
	"CountDeliveryEventsByStatus":            {1},
	"CreateApplicationCredential":            {3},
	"CreateComplianceGrant":                  {3},
	"CreateSession":                          {5},
	"InsertArchiveEntry":                     {7},
	"PlatformAIQuotaUsage":                   {3, 4},
	"PlatformAdvanceCollectorCheckpoint":     {5},
	"PlatformBindSchedulingEvent":            {5},
	"PlatformBrandHostCertificateTransition": {4},
	"PlatformCreateAnnouncement":             {4, 5},
	"PlatformCreateInvestigation":            {4},
	"PlatformCreateMailboxScheduledSend":     {5},
	"PlatformFindTLSReportFingerprint":       {4, 5},
	"PlatformIndexArchiveCopy":               {7},
	"PlatformInsertArchiveEntry":             {7},
	"PlatformInsertBrandHostChallenge":       {3},
	"PlatformInsertEvent":                    {7, 8},
	"PlatformListCalendarEventsPage":         {3, 4},
	"PlatformListEvents":                     {2, 3},
	"PlatformPutDeliveryQueueContext":        {7},
	"PlatformQueueAuthenticatedTenant":       {3},
	"PlatformRecordCollectedDeliveryEvent":   {13},
	"PlatformRecordTLSReport":                {5, 6},
	"PlatformRequeueMailboxScheduledSend":    {0},
	"PlatformReserveAIRequest":               {4, 5, 14},
	"PlatformReserveMailboxAIRequest":        {4, 5, 14},
	"PlatformScheduleCalendarReminder":       {4},
	"PlatformSetBrandHostLifecycle":          {3, 4, 5},
	"PlatformSumAIUsageBetween":              {1, 2},
	"PlatformUpdateAnnouncement":             {5, 6},
	"PlatformUpdateEvent":                    {3, 4},
	"PlatformVerifyBrandHostProof":           {1},
	"RecordDeliveryEvent":                    {13},
	"SetMailboxImportJob":                    {4},
	"TenantDeliveryOutcomeCounts":            {1, 2},
	"UpdateDomainStatus":                     {3},
}

func adaptPostgresArgs(method string, args []any) []any {
	indexes := postgresTimestampArgIndexes[method]
	if len(indexes) == 0 {
		return args
	}
	out := append([]any(nil), args...)
	for _, index := range indexes {
		if index >= len(out) {
			continue
		}
		if micros, ok := out[index].(int64); ok {
			out[index] = time.UnixMicro(micros).UTC()
		}
	}
	return out
}

type postgresDialectMarker interface{ isPostgresDialect() }
type sqliteDialectMarker interface{ IsSQLite() bool }

// PostgresCompatibleDBTX selects the frozen PostgreSQL sqlc statement for
// named methods while leaving SQLite DBTX handles on their native generated SQL.
// It never rewrites placeholders or translates SQL text.
func PostgresCompatibleDBTX(tx DBTX) DBTX {
	if tx == nil {
		panic("store.PostgresCompatibleDBTX: nil DBTX")
	}
	if marker, ok := tx.(sqliteDialectMarker); ok && marker.IsSQLite() {
		return tx
	}
	if _, ok := tx.(postgresDialectMarker); ok {
		return tx
	}
	return &pgDialectDBTX{inner: tx}
}

func isSQLiteDBTX(tx DBTX) bool {
	marker, ok := tx.(sqliteDialectMarker)
	return ok && marker.IsSQLite()
}

type pgDialectDBTX struct{ inner DBTX }

func (*pgDialectDBTX) isPostgresDialect() {}

func (p *pgDialectDBTX) statement(query string) (string, error) {
	method, named := sqlcQueryName(query)
	if !named {
		if sqliteOnlySQL.MatchString(query) {
			return "", fmt.Errorf("%w: unlabelled SQLite-only SQL refused on PostgreSQL", ErrPostgreSQLQueryUnavailable)
		}
		return query, nil
	}
	statement, ok := pgstore.QuerySQL(method)
	if !ok {
		if extra, ok := pgExtraStatements[method]; ok {
			return extra, nil
		}
		return "", fmt.Errorf("%w: %s", ErrPostgreSQLQueryUnavailable, method)
	}
	return statement, nil
}

func sqlcQueryName(query string) (string, bool) {
	const prefix = "-- name: "
	if !strings.HasPrefix(query, prefix) {
		return "", false
	}
	rest := query[len(prefix):]
	idx := strings.IndexAny(rest, " \t\r\n")
	if idx <= 0 {
		return "", false
	}
	return rest[:idx], true
}

func (p *pgDialectDBTX) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	statement, err := p.statement(query)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	method, _ := sqlcQueryName(query)
	return p.inner.Exec(ctx, statement, adaptPostgresArgs(method, args)...)
}

func (p *pgDialectDBTX) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	statement, err := p.statement(query)
	if err != nil {
		return nil, err
	}
	method, _ := sqlcQueryName(query)
	rows, err := p.inner.Query(ctx, statement, adaptPostgresArgs(method, args)...)
	if err != nil {
		return nil, err
	}
	return pgDialectRows{Rows: rows}, nil
}

func (p *pgDialectDBTX) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	statement, err := p.statement(query)
	if err != nil {
		return pgErrorRow{err: err}
	}
	method, _ := sqlcQueryName(query)
	rows, err := p.inner.Query(ctx, statement, adaptPostgresArgs(method, args)...)
	if err != nil {
		return pgErrorRow{err: err}
	}
	return pgDialectRow{rows: pgDialectRows{Rows: rows}}
}

// Begin preserves pgx transaction ownership while retaining the dialect adapter.
func (p *pgDialectDBTX) Begin(ctx context.Context) (pgx.Tx, error) {
	beginner, ok := p.inner.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return nil, errors.New("store: underlying PostgreSQL DBTX does not support Begin")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &pgDialectTx{Tx: tx, dbtx: &pgDialectDBTX{inner: tx}}, nil
}

type pgDialectTx struct {
	pgx.Tx
	dbtx *pgDialectDBTX
}

func (*pgDialectTx) isPostgresDialect() {}

func (t *pgDialectTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return t.dbtx.Exec(ctx, query, args...)
}
func (t *pgDialectTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	return t.dbtx.Query(ctx, query, args...)
}
func (t *pgDialectTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return t.dbtx.QueryRow(ctx, query, args...)
}
func (t *pgDialectTx) Begin(ctx context.Context) (pgx.Tx, error) {
	return t.dbtx.Begin(ctx)
}

// pgDialectRows converts PostgreSQL timestamps to the SQLite-model unix-micro
// representation only where the generated consumer asks for int64.
type pgDialectRows struct{ pgx.Rows }

func (r pgDialectRows) Scan(dest ...any) error {
	fields := r.FieldDescriptions()
	fixups := make([]func(), 0)
	for i, field := range fields {
		if i >= len(dest) {
			break
		}
		target, ok := dest[i].(*int64)
		if !ok {
			continue
		}
		switch field.DataTypeOID {
		case 1184:
			var value pgtype.Timestamptz
			dest[i] = &value
			fixups = append(fixups, func() {
				if value.Valid {
					*target = value.Time.UnixMicro()
				} else {
					*target = 0
				}
			})
		case 1114:
			var value pgtype.Timestamp
			dest[i] = &value
			fixups = append(fixups, func() {
				if value.Valid {
					*target = value.Time.UnixMicro()
				} else {
					*target = 0
				}
			})
		}
	}
	if err := r.Rows.Scan(dest...); err != nil {
		return err
	}
	for _, fixup := range fixups {
		fixup()
	}
	return nil
}

type pgDialectRow struct{ rows pgDialectRows }
type pgErrorRow struct{ err error }

func (r pgErrorRow) Scan(...any) error { return r.err }
func (r pgDialectRow) Scan(dest ...any) error {
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	return r.rows.Scan(dest...)
}
