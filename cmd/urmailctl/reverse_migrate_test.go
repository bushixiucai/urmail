package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestCutoverReverseMigrateBaselinePKsJSONAndDualWriteDetection(t *testing.T) {
	dir := t.TempDir()
	baseFile := filepath.Join(dir, "baseline-pks.json")
	if err := os.WriteFile(baseFile, []byte(`{"mailbox_aliases":["1","2"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadBaselinePKsJSON(baseFile)
	if err != nil {
		t.Fatalf("loadBaselinePKsJSON: %v", err)
	}
	if !loaded["mailbox_aliases"]["1"] || !loaded["mailbox_aliases"]["2"] {
		t.Fatalf("unexpected loaded baseline PKs: %+v", loaded)
	}

	// With baseline loaded: a post-baseline PG row absent from SQLite triggers dual-write abort.
	pgWithDualWrite := map[string]bool{"1": true, "2": true, "99": true}
	sqlitePKs := map[string]bool{"1": true, "2": true}
	if _, err := reversemigrate.ReconcileTableDelta("mailbox_aliases", loaded["mailbox_aliases"], pgWithDualWrite, sqlitePKs); err == nil || !strings.Contains(err.Error(), "dual-write") {
		t.Fatalf("expected dual-write abort with baseline loaded, got %v", err)
	}

	// Without baseline (nil): dual-write check is explicitly skipped per design.
	del, err := reversemigrate.ReconcileTableDelta("mailbox_aliases", nil, pgWithDualWrite, sqlitePKs)
	if err != nil || len(del) != 1 || del[0] != "99" {
		t.Fatalf("expected nil baseline to skip dual-write check and schedule delete [99], got del=%v err=%v", del, err)
	}

	// CLI flag --baseline-pks-json must load and accept valid baseline file in dry-run.
	hold := filepath.Join(dir, "upgrade.hold")
	if err := os.WriteFile(hold, []byte("gen-d1"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCutoverWithWriter(context.Background(), []string{
		"reverse-migrate",
		"--dry-run",
		"--hold-file", hold,
		"--listeners-stopped",
		"--writers-stopped",
		"--sqlite-wal-clean",
		"--pg-writers-zero",
		"--baseline-pks-json", baseFile,
	}, &out); err != nil {
		t.Fatalf("CLI with --baseline-pks-json failed: %v", err)
	}

	// Missing --baseline-pks-json path must fail closed.
	out.Reset()
	if err := runCutoverWithWriter(context.Background(), []string{
		"reverse-migrate",
		"--dry-run",
		"--hold-file", hold,
		"--listeners-stopped",
		"--writers-stopped",
		"--sqlite-wal-clean",
		"--pg-writers-zero",
		"--baseline-pks-json", filepath.Join(dir, "missing.json"),
	}, &out); err == nil {
		t.Fatalf("expected error on missing --baseline-pks-json")
	}
}
