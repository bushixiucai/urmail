// Package cutover implements the Phase 1 S8 PG->SQLite cutover state machine,
// dual-stack disk budget preflight, canary 4xx inbound policy, zero-loss
// rollback window, and post-window break-glass reverse export.
package cutover

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PGRetentionDays is the PG volume retention period for disk budgeting and
// forensics only; it is NOT a zero-loss rollback SLA.
const PGRetentionDays = 14

// CanaryInbound4xxCode and CanaryInbound4xxMessage enforce the mandatory
// temporary SMTP deferral during the read-only canary window.
const (
	CanaryInbound4xxCode    = 450
	CanaryInbound4xxMessage = "4.3.2 cutover canary in progress; please retry"
)

// InboundMode identifies how inbound SMTP is handled during canary.
type InboundMode string

// InboundMode values control inbound SMTP behavior during canary.
const (
	Inbound4xxDefer             InboundMode = "4xx-defer"
	InboundLegacyPGContinueRecv InboundMode = "pg-continue-receiving"
)

// Phase represents the ordered S8 cutover lifecycle.
type Phase string

// Phase values track the ordered cutover and rollback state machine.
const (
	PhaseIdle               Phase = "idle"
	PhaseFenced             Phase = "fenced"
	PhaseWritersStopped     Phase = "writers_stopped"
	PhaseMigrated           Phase = "migrated"
	PhaseCanary             Phase = "canary"
	PhaseReleased           Phase = "released"
	PhaseRolledBackZeroLoss Phase = "rolled_back_zero_loss"
	PhaseBreakGlassExported Phase = "break_glass_exported"
)

// DiskBudgetInput carries observed sizes for dual-stack disk verification.
type DiskBudgetInput struct {
	PGBytes        int64 `json:"pg_bytes"`
	SQLiteBytes    int64 `json:"sqlite_bytes"`
	SnapshotBytes  int64 `json:"snapshot_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
}

// DiskBudgetResult reports whether available space covers PG*1.1 + SQLite*2 + snapshot.
type DiskBudgetResult struct {
	PGBytes         int64  `json:"pg_bytes"`
	SQLiteBytes     int64  `json:"sqlite_bytes"`
	SnapshotBytes   int64  `json:"snapshot_bytes"`
	RequiredBytes   int64  `json:"required_bytes"`
	AvailableBytes  int64  `json:"available_bytes"`
	ShortfallBytes  int64  `json:"shortfall_bytes"`
	PGRetentionDays int    `json:"pg_retention_days"`
	RetentionNote   string `json:"retention_note"`
	Sufficient      bool   `json:"sufficient"`
}

// CheckDiskBudget enforces free >= ceil(PG*1.1) + SQLite*2 + snapshot.
func CheckDiskBudget(in DiskBudgetInput) (DiskBudgetResult, error) {
	if in.PGBytes < 0 || in.SQLiteBytes < 0 || in.SnapshotBytes < 0 || in.AvailableBytes < 0 {
		return DiskBudgetResult{}, errors.New("disk budget sizes must be non-negative")
	}
	pgWithMargin := in.PGBytes + (in.PGBytes+9)/10
	required := pgWithMargin + in.SQLiteBytes*2 + in.SnapshotBytes
	shortfall := int64(0)
	if in.AvailableBytes < required {
		shortfall = required - in.AvailableBytes
	}
	res := DiskBudgetResult{
		PGBytes:         in.PGBytes,
		SQLiteBytes:     in.SQLiteBytes,
		SnapshotBytes:   in.SnapshotBytes,
		RequiredBytes:   required,
		AvailableBytes:  in.AvailableBytes,
		ShortfallBytes:  shortfall,
		PGRetentionDays: PGRetentionDays,
		RetentionNote:   "14 days is PG volume retention for disk budget and forensics only, not a rollback SLA",
		Sufficient:      shortfall == 0,
	}
	if !res.Sufficient {
		return res, fmt.Errorf("dual-stack disk budget insufficient: need %d bytes (PG*1.1 + SQLite*2 + snapshot), have %d bytes (shortfall %d)",
			required, in.AvailableBytes, shortfall)
	}
	return res, nil
}

// State tracks one cutover execution and its zero-loss rollback window.
type State struct {
	Phase            Phase            `json:"phase"`
	InboundMode      InboundMode      `json:"inbound_mode"`
	Inbound4xxReply  string           `json:"inbound_4xx_reply"`
	WaiverRef        string           `json:"waiver_ref,omitempty"`
	DiskBudget       DiskBudgetResult `json:"disk_budget"`
	UpdatedAt        time.Time        `json:"updated_at"`
	CanaryStartedAt  time.Time        `json:"canary_started_at,omitempty"`
	FirstProdWriteAt time.Time        `json:"first_prod_write_at,omitempty"`
	RollbackAt       time.Time        `json:"rollback_at,omitempty"`
	BreakGlassPath   string           `json:"break_glass_path,omitempty"`
}

// ZeroLossWindowOpen returns true only before production writes are released.
func (s State) ZeroLossWindowOpen() bool {
	return s.FirstProdWriteAt.IsZero() && s.Phase != PhaseReleased && s.Phase != PhaseBreakGlassExported
}

// ValidateCanaryInbound enforces the S8 inbound MTA policy during canary.
func ValidateCanaryInbound(mode InboundMode, waiverRef string) (string, error) {
	switch mode {
	case "", Inbound4xxDefer:
		return fmt.Sprintf("%d %s", CanaryInbound4xxCode, CanaryInbound4xxMessage), nil
	case InboundLegacyPGContinueRecv:
		if waiverRef == "" {
			return "", errors.New("pg-continue-receiving breaks zero-loss semantics and is not implemented; requires signed cutover waiver")
		}
		return "", fmt.Errorf("pg-continue-receiving is not implemented in code; handle manually under signed waiver %q", waiverRef)
	default:
		return "", fmt.Errorf("unsupported canary inbound mode %q", mode)
	}
}

// LoadState reads the persisted cutover state, or returns an idle state if absent.
func LoadState(path string) (State, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-configured state path.
	if os.IsNotExist(err) {
		return State{Phase: PhaseIdle, InboundMode: Inbound4xxDefer}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("decode cutover state: %w", err)
	}
	return st, nil
}

// SaveState atomically persists cutover state.
func SaveState(path string, st State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	st.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "cutover-state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// EnterCanary transitions a migrated cutover into the read-only canary phase
// with the mandatory 4xx inbound deferral policy.
func EnterCanary(st State, now time.Time) (State, error) {
	if st.Phase != PhaseMigrated && st.Phase != PhaseFenced && st.Phase != PhaseWritersStopped {
		return st, fmt.Errorf("cannot enter canary from phase %q", st.Phase)
	}
	reply, err := ValidateCanaryInbound(st.InboundMode, st.WaiverRef)
	if err != nil {
		return st, err
	}
	st.Phase = PhaseCanary
	st.InboundMode = Inbound4xxDefer
	st.Inbound4xxReply = reply
	st.CanaryStartedAt = now.UTC()
	return st, nil
}

// ReleaseProductionWrites closes the zero-loss rollback window permanently.
func ReleaseProductionWrites(st State, now time.Time) (State, error) {
	if st.Phase != PhaseCanary {
		return st, fmt.Errorf("can only release production writes from canary phase, got %q", st.Phase)
	}
	st.Phase = PhaseReleased
	st.FirstProdWriteAt = now.UTC()
	return st, nil
}

// RollbackZeroLoss performs a zero-loss rollback to PG while inside the canary window.
// After ReleaseProductionWrites, zero-loss rollback is refused fail-closed.
func RollbackZeroLoss(st State, now time.Time) (State, error) {
	if !st.ZeroLossWindowOpen() {
		return st, fmt.Errorf("zero-loss rollback window is closed (released at %s); only manual break-glass reverse export is available with no SLA",
			st.FirstProdWriteAt.Format(time.RFC3339))
	}
	if st.Phase == PhaseIdle || st.Phase == PhaseRolledBackZeroLoss {
		return st, fmt.Errorf("cannot rollback from phase %q", st.Phase)
	}
	st.Phase = PhaseRolledBackZeroLoss
	st.RollbackAt = now.UTC()
	return st, nil
}

// BreakGlassReport records the manual reverse thin-channel export after the
// zero-loss rollback window has closed.
type BreakGlassReport struct {
	ExportedAt      time.Time      `json:"exported_at"`
	SourceSQLite    string         `json:"source_sqlite"`
	OutFile         string         `json:"out_file"`
	NoSLACommitment bool           `json:"no_sla_commitment"`
	Tables          map[string]int `json:"tables"`
}

// BreakGlassReverseExport exports user-visible SQLite rows into a JSON bundle
// for manual PostgreSQL break-glass recovery (no SLA commitment).
func BreakGlassReverseExport(ctx context.Context, db *sql.DB, sourcePath, outPath string) (BreakGlassReport, error) {
	if db == nil {
		return BreakGlassReport{}, errors.New("break-glass export requires open SQLite database")
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return BreakGlassReport{}, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return BreakGlassReport{}, err
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		return BreakGlassReport{}, err
	}

	payload := struct {
		ExportedAt      time.Time                   `json:"exported_at"`
		NoSLACommitment bool                        `json:"no_sla_commitment"`
		SourceSQLite    string                      `json:"source_sqlite"`
		Data            map[string][]map[string]any `json:"data"`
	}{
		ExportedAt:      time.Now().UTC(),
		NoSLACommitment: true,
		SourceSQLite:    sourcePath,
		Data:            make(map[string][]map[string]any, len(tables)),
	}
	counts := make(map[string]int, len(tables))

	for _, table := range tables {
		tRows, qErr := db.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM "%s"`, table)) // #nosec G202 -- table names come strictly from sqlite_master.
		if qErr != nil {
			return BreakGlassReport{}, qErr
		}
		cols, cErr := tRows.Columns()
		if cErr != nil {
			_ = tRows.Close()
			return BreakGlassReport{}, cErr
		}
		var records []map[string]any
		for tRows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if sErr := tRows.Scan(ptrs...); sErr != nil {
				_ = tRows.Close()
				return BreakGlassReport{}, sErr
			}
			rec := make(map[string]any, len(cols))
			for i, col := range cols {
				if b, ok := vals[i].([]byte); ok {
					rec[col] = string(b)
				} else {
					rec[col] = vals[i]
				}
			}
			records = append(records, rec)
		}
		if err := tRows.Close(); err != nil {
			return BreakGlassReport{}, err
		}
		payload.Data[table] = records
		counts[table] = len(records)
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		return BreakGlassReport{}, err
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return BreakGlassReport{}, err
	}
	if err := os.WriteFile(outPath, append(encoded, '\n'), 0o600); err != nil {
		return BreakGlassReport{}, err
	}
	return BreakGlassReport{
		ExportedAt:      payload.ExportedAt,
		SourceSQLite:    sourcePath,
		OutFile:         outPath,
		NoSLACommitment: true,
		Tables:          counts,
	}, nil
}
