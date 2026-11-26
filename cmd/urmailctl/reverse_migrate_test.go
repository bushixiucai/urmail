package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/reversemigrate"
)

func TestCutoverReverseMigrateCLIDryRunAndFailClosed(t *testing.T) {
	dir := t.TempDir()
	hold := filepath.Join(dir, "upgrade.hold")
	if err := os.WriteFile(hold, []byte("gen-cli"), 0o600); err != nil {
		t.Fatal(err)
	}
	srcMail := filepath.Join(dir, "src-mail")
	tgtMail := filepath.Join(dir, "tgt-mail")
	if err := os.MkdirAll(filepath.Join(srcMail, "u1/Maildir/cur"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tgtMail, "u1/Maildir/cur"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcMail, "u1/Maildir/cur/m1:2,S"), []byte("mail-body"), 0o640); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runCutoverWithWriter(context.Background(), []string{
		"reverse-migrate",
		"--dry-run",
		"--hold-file", hold,
		"--listeners-stopped",
		"--writers-stopped",
		"--sqlite-wal-clean",
		"--pg-writers-zero",
		"--src-maildir", srcMail,
		"--tgt-maildir", tgtMail,
	}, &out)
	if err != nil {
		t.Fatalf("CLI dry-run failed: %v (out=%s)", err, out.String())
	}
	var rep reversemigrate.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if !rep.DryRun || !rep.NoSLACommitment || rep.Maildir == nil || rep.Maildir.FilesCopied != 1 {
		t.Fatalf("unexpected CLI report: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(tgtMail, "u1/Maildir/cur/m1:2,S")); !os.IsNotExist(err) {
		t.Fatalf("dry-run mutated target maildir")
	}

	// Negative: omitting --writers-stopped must fail closed.
	out.Reset()
	err = runCutoverWithWriter(context.Background(), []string{
		"reverse-migrate",
		"--dry-run",
		"--hold-file", hold,
		"--listeners-stopped",
		"--sqlite-wal-clean",
		"--pg-writers-zero",
	}, &out)
	if err == nil {
		t.Fatalf("expected fail-closed error when writers not stopped")
	}
}
