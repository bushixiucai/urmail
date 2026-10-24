package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/bushixiucai/urmail/internal/miadbinventory"
)

func runImportMIADB(ctx context.Context, args []string) error {
	return importMIADBDryRun(ctx, args, os.Stdout)
}

func importMIADBDryRun(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("import-miadb", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options miadbinventory.Options
	flags.StringVar(&options.ConfigPath, "config", "", "explicit legacy mailinabox.conf")
	flags.StringVar(&options.SQLitePath, "sqlite", "", "explicit consistent legacy users.sqlite")
	flags.StringVar(&options.MappingPath, "mapping", "", "private explicit domain-to-tenant mapping JSON")
	dryRun := flags.Bool("dry-run", false, "inspect only; no target writes or apply operation")
	if flags.Parse(args) != nil {
		return errors.New("invalid import-miadb arguments; only explicit read-only dry-run is supported")
	}
	if flags.NArg() != 0 || !*dryRun || options.ConfigPath == "" || options.SQLitePath == "" {
		return errors.New("import-miadb requires --dry-run --config FILE --sqlite FILE; apply is not implemented")
	}
	report, err := miadbinventory.Inspect(ctx, options)
	if err != nil {
		return err
	}
	if err = json.NewEncoder(output).Encode(report); err != nil {
		return err
	}
	if report.Status != "dry_run_complete" {
		return errors.New("MIAB dry-run is blocked; resolve the reported issues; no target changed")
	}
	return nil
}
