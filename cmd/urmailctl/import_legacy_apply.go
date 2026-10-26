package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/miabimport"
	"github.com/bushixiucai/urmail/internal/miadbinventory"
)

var errLegacyImportCLI = errors.New("LEGACY_IMPORT_CLI_REJECTED")

func runImportLegacyCore(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 {
		return miabimport.ErrAuthority
	}
	flags := flag.NewFlagSet("import-legacy-core", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var (
		configPath, sqlitePath, mappingPath, reason, expected string
		tenantID, actorID                                     int64
		apply                                                 bool
	)
	flags.BoolVar(&apply, "apply", false, "commit reviewed core metadata into an empty tenant")
	flags.StringVar(&configPath, "config", "", "explicit legacy mailinabox.conf")
	flags.StringVar(&sqlitePath, "sqlite", "", "explicit consistent users.sqlite")
	flags.StringVar(&mappingPath, "mapping", "", "private reviewed mapping JSON")
	flags.StringVar(&expected, "expected-snapshot", "", "reviewed snapshot SHA256")
	flags.StringVar(&reason, "reason", "", "operator reason")
	flags.Int64Var(&tenantID, "tenant-id", 0, "existing empty tenant id")
	flags.Int64Var(&actorID, "actor-id", 0, "platform admin actor id")
	tenantRef := flags.String("tenant-ref", "", "mapping tenant_ref")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !apply || configPath == "" || sqlitePath == "" || mappingPath == "" || expected == "" || reason == "" || tenantID <= 0 || actorID <= 0 || *tenantRef == "" {
		return errLegacyImportCLI
	}
	source, err := miadbinventory.LoadReviewedSource(ctx, miadbinventory.ReviewedSourceOptions{
		Options:                miadbinventory.Options{ConfigPath: configPath, SQLitePath: sqlitePath, MappingPath: mappingPath},
		ExpectedSnapshotSHA256: expected,
	})
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return errLegacyImportCLI
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return errLegacyImportCLI
	}
	defer pool.Close()
	result, err := miabimport.ApplyCore(ctx, pool, source, miabimport.Options{TenantID: tenantID, ActorID: actorID, TenantRef: *tenantRef, Reason: reason})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func runImportLegacyActivateMail(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 {
		return miabimport.ErrAuthority
	}
	flags := flag.NewFlagSet("import-legacy-activate-mail", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var (
		reason, coreProof string
		tenantID, actorID int64
		expected          int
		apply             bool
	)
	flags.BoolVar(&apply, "apply", false, "promote delegated pending domains to active+ready for mail")
	flags.StringVar(&coreProof, "expected-core-proof", "", "committed core private_proof_sha256")
	flags.StringVar(&reason, "reason", "", "operator reason")
	flags.Int64Var(&tenantID, "tenant-id", 0, "tenant id with committed core+dkim import")
	flags.Int64Var(&actorID, "actor-id", 0, "platform admin actor id")
	flags.IntVar(&expected, "expected-domains", 0, "exact domain count to activate")
	tenantRef := flags.String("tenant-ref", "", "mapping tenant_ref")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !apply || coreProof == "" || reason == "" || tenantID <= 0 || actorID <= 0 || expected <= 0 || *tenantRef == "" {
		return errLegacyImportCLI
	}
	cfg, err := config.Load()
	if err != nil {
		return errLegacyImportCLI
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return errLegacyImportCLI
	}
	defer pool.Close()
	result, err := miabimport.ActivateLegacyDelegatedMail(ctx, pool, miabimport.Options{
		TenantID: tenantID, ActorID: actorID, TenantRef: *tenantRef, Reason: reason,
	}, coreProof, expected)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func runImportLegacyDKIM(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 {
		return miabimport.ErrAuthority
	}
	flags := flag.NewFlagSet("import-legacy-dkim", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var (
		configPath, sqlitePath, mappingPath, cloneRoot, reason, expected string
		privateSHA, keyTableSHA, signingSHA                              string
		tenantID, actorID                                                int64
		apply                                                            bool
	)
	flags.BoolVar(&apply, "apply", false, "persist exact reviewed DKIM private key")
	flags.StringVar(&configPath, "config", "", "explicit legacy mailinabox.conf")
	flags.StringVar(&sqlitePath, "sqlite", "", "explicit consistent users.sqlite")
	flags.StringVar(&mappingPath, "mapping", "", "private reviewed mapping JSON")
	flags.StringVar(&cloneRoot, "clone-root", "", "private DKIM file clone root")
	flags.StringVar(&expected, "expected-snapshot", "", "reviewed core snapshot SHA256")
	flags.StringVar(&privateSHA, "expected-private", "", "mail.private SHA256")
	flags.StringVar(&keyTableSHA, "expected-keytable", "", "KeyTable SHA256")
	flags.StringVar(&signingSHA, "expected-signing", "", "SigningTable SHA256")
	flags.StringVar(&reason, "reason", "", "operator reason")
	flags.Int64Var(&tenantID, "tenant-id", 0, "tenant id with committed core import")
	flags.Int64Var(&actorID, "actor-id", 0, "platform admin actor id")
	tenantRef := flags.String("tenant-ref", "", "mapping tenant_ref")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !apply || configPath == "" || sqlitePath == "" || mappingPath == "" || cloneRoot == "" || expected == "" || privateSHA == "" || keyTableSHA == "" || signingSHA == "" || reason == "" || tenantID <= 0 || actorID <= 0 || *tenantRef == "" {
		return errLegacyImportCLI
	}
	source, err := miadbinventory.LoadReviewedSource(ctx, miadbinventory.ReviewedSourceOptions{
		Options:                miadbinventory.Options{ConfigPath: configPath, SQLitePath: sqlitePath, MappingPath: mappingPath},
		ExpectedSnapshotSHA256: expected,
	})
	if err != nil {
		return err
	}
	dkim, err := miabimport.LoadReviewedDKIM(ctx, miabimport.DKIMSourceOptions{
		CloneRoot:                  cloneRoot,
		ExpectedPrivateSHA256:      privateSHA,
		ExpectedKeyTableSHA256:     keyTableSHA,
		ExpectedSigningTableSHA256: signingSHA,
		ExpectedSnapshotSHA256:     expected,
	}, source)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return errLegacyImportCLI
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return errLegacyImportCLI
	}
	defer pool.Close()
	result, err := miabimport.ApplyReviewedDKIM(ctx, pool, dkim, miabimport.Options{TenantID: tenantID, ActorID: actorID, TenantRef: *tenantRef, Reason: reason})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
