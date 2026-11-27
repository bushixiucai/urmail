package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bushixiucai/urmail/internal/cutover"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

const defaultCutoverStatePath = "/var/lib/urmail-backup/cutover-state.json"

func runCutover(ctx context.Context, args []string) error {
	return runCutoverWithWriter(ctx, args, os.Stdout)
}

func runCutoverWithWriter(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: urmailctl cutover <disk-budget | canary | release | rollback | break-glass | reverse-migrate>")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "disk-budget":
		return runCutoverDiskBudget(rest, out)
	case "canary":
		return runCutoverCanary(rest, out)
	case "release":
		return runCutoverRelease(rest, out)
	case "rollback":
		return runCutoverRollback(rest, out)
	case "break-glass":
		return runCutoverBreakGlass(ctx, rest, out)
	case "reverse-migrate":
		return runCutoverReverseMigrate(ctx, rest, out)
	default:
		return fmt.Errorf("unknown cutover subcommand %q", sub)
	}
}

func availableBytesForPath(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * st.Bsize, nil // #nosec G115 -- filesystem block stats fit signed int64 on supported volumes.
}

func runCutoverDiskBudget(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("cutover disk-budget", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	pgBytes := flags.Int64("pg-bytes", 0, "PostgreSQL data volume size in bytes")
	sqliteBytes := flags.Int64("sqlite-bytes", 0, "SQLite database size in bytes")
	snapshotBytes := flags.Int64("snapshot-bytes", 0, "Dovecot auth + backup snapshot size in bytes")
	availBytes := flags.Int64("available-bytes", -1, "override available free bytes (default: statfs --path)")
	path := flags.String("path", "/srv/urmail", "target filesystem path for free space check")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: urmailctl cutover disk-budget --pg-bytes N --sqlite-bytes N --snapshot-bytes N [--available-bytes N | --path DIR]")
	}
	avail := *availBytes
	if avail < 0 {
		v, err := availableBytesForPath(*path)
		if err != nil {
			return fmt.Errorf("statfs %s: %w", *path, err)
		}
		avail = v
	}
	res, checkErr := cutover.CheckDiskBudget(cutover.DiskBudgetInput{
		PGBytes:        *pgBytes,
		SQLiteBytes:    *sqliteBytes,
		SnapshotBytes:  *snapshotBytes,
		AvailableBytes: avail,
	})
	encoded, _ := json.Marshal(res)
	_, _ = fmt.Fprintln(out, string(encoded))
	return checkErr
}

func runCutoverCanary(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("cutover canary", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateFile := flags.String("state-file", defaultCutoverStatePath, "cutover state file")
	inboundMode := flags.String("inbound-mode", string(cutover.Inbound4xxDefer), "canary inbound SMTP mode (default 4xx-defer)")
	waiverRef := flags.String("waiver-ref", "", "signed waiver id if overriding default")
	seedPhase := flags.String("from-phase", string(cutover.PhaseMigrated), "starting phase when initializing state")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: urmailctl cutover canary [--state-file PATH] [--inbound-mode 4xx-defer]")
	}
	st, err := cutover.LoadState(*stateFile)
	if err != nil {
		return err
	}
	if st.Phase == cutover.PhaseIdle {
		st.Phase = cutover.Phase(*seedPhase)
	}
	st.InboundMode = cutover.InboundMode(*inboundMode)
	st.WaiverRef = *waiverRef
	st, err = cutover.EnterCanary(st, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := cutover.SaveState(*stateFile, st); err != nil {
		return err
	}
	encoded, _ := json.Marshal(st)
	_, _ = fmt.Fprintln(out, string(encoded))
	return nil
}

func runCutoverRelease(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("cutover release", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateFile := flags.String("state-file", defaultCutoverStatePath, "cutover state file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: urmailctl cutover release [--state-file PATH]")
	}
	st, err := cutover.LoadState(*stateFile)
	if err != nil {
		return err
	}
	st, err = cutover.ReleaseProductionWrites(st, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := cutover.SaveState(*stateFile, st); err != nil {
		return err
	}
	encoded, _ := json.Marshal(st)
	_, _ = fmt.Fprintln(out, string(encoded))
	return nil
}

func runCutoverRollback(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("cutover rollback", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateFile := flags.String("state-file", defaultCutoverStatePath, "cutover state file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: urmailctl cutover rollback [--state-file PATH]")
	}
	st, err := cutover.LoadState(*stateFile)
	if err != nil {
		return err
	}
	st, err = cutover.RollbackZeroLoss(st, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := cutover.SaveState(*stateFile, st); err != nil {
		return err
	}
	encoded, _ := json.Marshal(st)
	_, _ = fmt.Fprintln(out, string(encoded))
	return nil
}

func runCutoverBreakGlass(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("cutover break-glass", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sqlitePath := flags.String("sqlite", "", "SQLite database path to reverse-export")
	outPath := flags.String("out", "", "output JSON bundle path")
	stateFile := flags.String("state-file", defaultCutoverStatePath, "cutover state file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: urmailctl cutover break-glass --sqlite PATH --out PATH [--state-file PATH]")
	}
	if !filepath.IsAbs(*sqlitePath) || !filepath.IsAbs(*outPath) {
		return fmt.Errorf("cutover break-glass requires absolute --sqlite and --out paths")
	}
	raw, err := sqlitedb.OpenExisting(ctx, *sqlitePath, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		return err
	}
	defer func() { _ = raw.Close() }()

	rep, err := cutover.BreakGlassReverseExport(ctx, raw, *sqlitePath, *outPath)
	if err != nil {
		return err
	}
	if st, loadErr := cutover.LoadState(*stateFile); loadErr == nil {
		st.Phase = cutover.PhaseBreakGlassExported
		st.BreakGlassPath = *outPath
		_ = cutover.SaveState(*stateFile, st)
	}
	encoded, _ := json.Marshal(rep)
	_, _ = fmt.Fprintln(out, string(encoded))
	return nil
}
