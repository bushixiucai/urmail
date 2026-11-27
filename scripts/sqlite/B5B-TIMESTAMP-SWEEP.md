# B5b — timestamp sweep to int64 unix microseconds

Scope: `fix/phase1-s2-queries`, after B5a (`2197845d`). This is the call-site half
of the engine switch: the SQLite schema stores every timestamp as
`INTEGER` unix micros, so the Go surface must stop carrying `time.Time` /
`pgtype.Timestamptz` wrappers through store params and scan targets.

## Convention

- **Storage and Go transport type**: `int64` unix microseconds, UTC.
  `0` means "zero value" and is the port of `pgtype.Timestamptz{Valid:false}`
  plus `time.Time{}`.
- **Conversion**: `timeutil.Micro(t)` on the way in, `timeutil.FromMicro(m)`
  on the way out. `timeutil.NowMicro()` when a store-facing "now" is needed
  directly.
- **Wire format is not changed**: API/DTO structs keep `time.Time`, because
  `encoding/json` renders it as RFC3339. Handlers convert at the mapping site
  (`timeutil.FromMicro(...)`), so the JSON contract (and
  `response_shape_test`) is untouched. Internal structs, params and scan
  targets use `int64`.
- **Nullable timestamps** are `int64` too (`0` instead of a wrapper). The
  override generator forces this for every column in
  `scripts/sqlite/timestamp-columns.txt` (217 columns, nullability ignored),
  which is why `models.go` no longer contains `pgtype.Timestamptz`.

## What changed

- `sqlc.yaml` overrides: `scripts/sqlite/gen_sqlc_overrides.py` now reads
  `scripts/sqlite/timestamp-columns.txt` and emits `go_type: "int64"` for all
  217 timestamp columns (regenerate with `--check` in CI). `models.go` and
  `db.go` stay intentionally unregenerated for the B5a reason (held shape /
  Phase 2 DBTX wiring).
- Generated query files: 175 `pgtype.Timestamptz` → `int64` scan targets, and
  now-unused `pgtype` imports removed. `scripts/sqlite/gen_sqlc.py --check`
  still reports "output up to date", so the change is reproducible.
- Handwritten layer: `sqlite_params.go` / `sqlite_methods.go` row and arg
  structs use `int64`; `timestamptzMicros`, `reportWindowMicro` and the
  marketing lease reads take micros directly.
- Call sites: ~95 production files and the test fixtures now convert at the
  boundary. Helpers that used to wrap time were rewritten with the same name
  where they are private (`timestamptz`, `pgtypeT`, `pgTime`, `ts`, `tz`,
  `shapeTime`, `timestamptzTime`).
- Two response DTOs that deliberately keep `time.Time`:
  `AliasResponse.CreatedAt`, `ArchiveResponse.ReceivedAt`, `RecallResponse.*`,
  `DomainResponse.CreatedAt`, `MailboxResponse.CreatedAt`,
  `PlatformTenant.CreatedAt`, `TenantResponse.CreatedAt`,
  `auditResponse.CreatedAt`, `ManagedBrand.CreatedAt` — all fill via
  `timeutil.FromMicro`.
- `timestamptzMicros(0)` now reports "not set"; `reportWindowMicro` rejects a
  zero bound instead of widening the scan.
- `scripts/sqlite/fix_timestamp_selectors.py` is the compiler-driven codemod
  used for the `.Valid` / `.Time` tails (it only rewrites selectors the
  compiler reports on an `int64`). It is a one-off migration aid, not part of
  CI.

## Nullable-timestamp obligation (Phase 2, must not be dropped silently)

The SQLite schema keeps nullable timestamp columns as `NULL` (triggers depend
on `IS NULL` / `IS NOT NULL`: e.g. `0001_init.sql`'s recall preview guard reads
`confirmed_at IS NOT NULL`, and the audit split keys on `preview_actor_id`).
The Go side now treats `0` as the zero value, so any generated query that
**projects** a nullable timestamp must return `coalesce(col, 0)` before the
scan target (`int64`) can accept it; scanning SQL `NULL` into `int64` fails.

Current inventory (2026-11-20):

```
$ python3 scripts/sqlite/nullable_timestamp_scan.py
24 nullable timestamp columns bare-projected by 134 statements
```

- The schema has 67 nullable timestamp columns; 24 of them are projected bare
  by 134 generated statements (e.g. `recall_requests.confirmed_at`,
  `mailbox_jobs.lease_until`, `compliance_grants.expires_at`,
  `sessions.mfa_verified_at`, `mailboxes.usage_measured_at`). The scanner
  (`scripts/sqlite/nullable_timestamp_scan.py`) pairs each statement's
  projection list with its generated scan targets, so the 134 is a real
  count, not a field-name grep.
- Wrapping the projection (`coalesce(col, 0) AS col`) is the lowest-risk
  option, but it changes the generated row types, so it must be done as part
  of the wiring pass with sqlc regeneration and the full gate, not as a
  mechanical append here.
- They are **not reachable on SQLite today**: `sqlitedb` is not wired into the
  daemon (Phase 2), and the store's `DBTX` is still the pgx surface
  (`internal/store/db.go`, held by B5a). The Go suite therefore cannot catch
  this class; the full suite is green on compile/unit tests only.
- Wiring owner: Phase 2 SQLite adapter. Either add `coalesce(col, 0)` to the
  projections, or make the adapter map SQL `NULL` to `0` for `int64`
  destinations. Choose one globally; do not mix, and do not make the columns
  `NOT NULL DEFAULT 0` — that would silently change trigger behaviour.

## Claim concurrency evidence

`internal/store/sqlite_claim_concurrency_test.go` (real SQLite file, WAL,
`BEGIN IMMEDIATE` through the `sqlitedb` DSN):

- `TestQuotaClaimAllowsExactlyOneWinner` — two goroutines against
  `takeMailboxQuotaSQL` with `mailbox_daily_send_limit=1`; exactly one
  succeeds and `messages_sent` stays 1.
- `TestQuotaClaimBothSucceedUnderHigherLimit` — control at limit 2; both
  succeed and the counter is 2.
- `TestEASClaimIsSingleWinner` — two agents race the generated `claimEASSend`
  (`ON CONFLICT DO NOTHING RETURNING`); exactly one row is created.

The tests execute the same SQL text the store will run, not a paraphrase,
because `store.New` still requires the pgx `DBTX`. Repeated runs:
`go test ./internal/store/ -run Claim -count=20` and `-race -count=5`.