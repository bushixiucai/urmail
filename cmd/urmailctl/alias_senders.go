package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/bushixiucai/urmail/internal/aliassenders"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
)

var errAliasSenderCLI = errors.New("ALIAS_SENDERS_CLI_REJECTED")

// runAliasSenders defaults to preview and never prints private route addresses.
func runAliasSenders(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 {
		return aliassenders.ErrAuthority
	}
	flags := flag.NewFlagSet("alias-senders", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("input", "", "private JSON intent file")
	apply := flags.Bool("apply", false, "apply an exactly reviewed preview")
	expected := flags.String("expected-preview", "", "review SHA256 required for apply")
	private := flags.String("private-review", "", "exclusive private review output file")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *input == "" || (*apply && *expected == "") || (!*apply && *expected != "") || (*apply && *private != "") {
		return errAliasSenderCLI
	}
	intent, err := readAliasSenderIntent(*input)
	if err != nil {
		return errAliasSenderCLI
	}
	cfg, err := config.Load()
	if err != nil {
		return errAliasSenderCLI
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return errAliasSenderCLI
	}
	defer pool.Close()
	if *apply {
		result, e := aliassenders.Apply(ctx, pool, intent, *expected)
		if e != nil {
			return e
		}
		if json.NewEncoder(os.Stdout).Encode(result) != nil {
			return errAliasSenderCLI
		}
		return nil
	}
	review, err := aliassenders.Preview(ctx, pool, intent)
	if err != nil {
		return err
	}
	if *private != "" {
		if err = writeAliasSenderReview(*private, review); err != nil {
			return errAliasSenderCLI
		}
	}
	if json.NewEncoder(os.Stdout).Encode(aliassenders.Result{Code: "ALIAS_SENDERS_PREVIEW", Digest: review.Digest, PreviousCount: len(review.Alias.SenderLogins), SelectedCount: len(review.Intent.SenderLogins)}) != nil {
		return errAliasSenderCLI
	}
	return nil
}

func readAliasSenderIntent(path string) (aliassenders.Intent, error) {
	var intent aliassenders.Intent
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 65536 {
		return intent, errAliasSenderCLI
	}
	// The root operator explicitly selects this bounded regular 0600 input;
	// descriptor identity is checked against the preceding non-symlink lstat.
	file, err := os.Open(path) //nolint:gosec // G304: deliberate private operator input.
	if err != nil {
		return intent, errAliasSenderCLI
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Mode().Perm() != 0o600 {
		return intent, errAliasSenderCLI
	}
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(raw) > 65536 {
		return intent, errAliasSenderCLI
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&intent) != nil {
		return intent, errAliasSenderCLI
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return intent, errAliasSenderCLI
	}
	return intent, nil
}

func writeAliasSenderReview(path string, review aliassenders.Review) error {
	// The explicit root-selected output is exclusively created, never replaced
	// or followed through a destination symlink, and contains the private review.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: deliberate exclusive private review output.
	if err != nil {
		return errAliasSenderCLI
	}
	success := false
	defer func() {
		_ = file.Close()
		if !success {
			_ = os.Remove(path)
		}
	}()
	if file.Chmod(0o600) != nil || json.NewEncoder(file).Encode(review) != nil || file.Sync() != nil {
		return errAliasSenderCLI
	}
	if file.Close() != nil {
		return errAliasSenderCLI
	}
	success = true
	return nil
}
