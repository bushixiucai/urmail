package cutover

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCheckDiskBudgetFormula(t *testing.T) {
	// PG=1000 -> ceil(1.1*1000)=1100; SQLite=500*2=1000; Snapshot=200 => required=2300.
	res, err := CheckDiskBudget(DiskBudgetInput{
		PGBytes:        1000,
		SQLiteBytes:    500,
		SnapshotBytes:  200,
		AvailableBytes: 2300,
	})
	if err != nil || !res.Sufficient || res.RequiredBytes != 2300 || res.PGRetentionDays != 14 {
		t.Fatalf("expected sufficient budget=2300, got res=%+v err=%v", res, err)
	}

	res, err = CheckDiskBudget(DiskBudgetInput{
		PGBytes:        1000,
		SQLiteBytes:    500,
		SnapshotBytes:  200,
		AvailableBytes: 2299,
	})
	if err == nil || res.Sufficient || res.ShortfallBytes != 1 {
		t.Fatalf("expected insufficient budget shortfall=1, got res=%+v err=%v", res, err)
	}
}

func TestCanaryWindowRollbackAndBreakGlass(t *testing.T) {
	now := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	st := State{Phase: PhaseMigrated, InboundMode: Inbound4xxDefer}

	// Entering canary sets 450 4xx deferral reply and keeps zero-loss window open.
	st, err := EnterCanary(st, now)
	if err != nil {
		t.Fatalf("EnterCanary: %v", err)
	}
	if !strings.HasPrefix(st.Inbound4xxReply, "450 ") || !st.ZeroLossWindowOpen() {
		t.Fatalf("unexpected canary state: %+v", st)
	}

	// Rolling back inside canary window succeeds with zero-loss phase.
	rb, err := RollbackZeroLoss(st, now.Add(5*time.Minute))
	if err != nil || rb.Phase != PhaseRolledBackZeroLoss {
		t.Fatalf("expected zero-loss rollback in canary window, got rb=%+v err=%v", rb, err)
	}

	// Releasing production writes permanently closes the zero-loss rollback window.
	released, err := ReleaseProductionWrites(st, now.Add(10*time.Minute))
	if err != nil || released.ZeroLossWindowOpen() {
		t.Fatalf("expected closed window after release, got %+v err=%v", released, err)
	}
	if _, err := RollbackZeroLoss(released, now.Add(11*time.Minute)); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("expected post-release zero-loss rollback refusal, got %v", err)
	}

	// Legacy PG-continue-receiving must remain unimplemented / rejected.
	if _, err := ValidateCanaryInbound(InboundLegacyPGContinueRecv, ""); err == nil {
		t.Fatal("expected pg-continue-receiving without waiver to fail")
	}
	if _, err := ValidateCanaryInbound(InboundLegacyPGContinueRecv, "WAIVER-001"); err == nil {
		t.Fatal("expected pg-continue-receiving to remain unimplemented even with waiver")
	}
}

func TestBreakGlassReverseExport(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "live.sqlite")
	outPath := filepath.Join(dir, "breakglass.json")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(context.Background(), `CREATE TABLE items(id INTEGER PRIMARY KEY, val TEXT); INSERT INTO items(id, val) VALUES(1, 'hello');`); err != nil {
		t.Fatal(err)
	}
	rep, err := BreakGlassReverseExport(context.Background(), db, dbPath, outPath)
	if err != nil {
		t.Fatalf("BreakGlassReverseExport: %v", err)
	}
	if !rep.NoSLACommitment || rep.Tables["items"] != 1 {
		t.Fatalf("unexpected break-glass report: %+v", rep)
	}
	if info, err := os.Stat(outPath); err != nil || info.Size() == 0 {
		t.Fatalf("expected break-glass file at %s: %v", outPath, err)
	}
}
