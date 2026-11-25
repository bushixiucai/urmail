package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/cutover"
)

func TestCutoverCLILifecycleAndDiskBudget(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "cutover-state.json")
	var out bytes.Buffer

	// 1. Disk budget pass and fail-closed.
	if err := runCutoverWithWriter(t.Context(), []string{
		"disk-budget", "--pg-bytes", "1000", "--sqlite-bytes", "500", "--snapshot-bytes", "200", "--available-bytes", "2300",
	}, &out); err != nil {
		t.Fatalf("disk-budget pass: %v", err)
	}
	out.Reset()
	if err := runCutoverWithWriter(t.Context(), []string{
		"disk-budget", "--pg-bytes", "1000", "--sqlite-bytes", "500", "--snapshot-bytes", "200", "--available-bytes", "2299",
	}, &out); err == nil {
		t.Fatal("expected insufficient disk budget to fail")
	}

	// 2. Enter canary (4xx defer) -> rollback in window succeeds.
	out.Reset()
	if err := runCutoverWithWriter(t.Context(), []string{"canary", "--state-file", stateFile}, &out); err != nil {
		t.Fatalf("canary: %v", err)
	}
	var st cutover.State
	if err := json.Unmarshal(out.Bytes(), &st); err != nil || !strings.HasPrefix(st.Inbound4xxReply, "450 ") {
		t.Fatalf("unexpected canary output: %s (%v)", out.String(), err)
	}
	out.Reset()
	if err := runCutoverWithWriter(t.Context(), []string{"rollback", "--state-file", stateFile}, &out); err != nil {
		t.Fatalf("in-window rollback: %v", err)
	}

	// 3. Re-enter canary -> release -> post-release zero-loss rollback refused -> break-glass succeeds.
	stateFile2 := filepath.Join(dir, "cutover-state-2.json")
	out.Reset()
	if err := runCutoverWithWriter(t.Context(), []string{"canary", "--state-file", stateFile2}, &out); err != nil {
		t.Fatalf("canary2: %v", err)
	}
	out.Reset()
	if err := runCutoverWithWriter(t.Context(), []string{"release", "--state-file", stateFile2}, &out); err != nil {
		t.Fatalf("release: %v", err)
	}
	out.Reset()
	if err := runCutoverWithWriter(t.Context(), []string{"rollback", "--state-file", stateFile2}, &out); err == nil {
		t.Fatal("expected post-release zero-loss rollback to fail")
	}

	dbPath := filepath.Join(dir, "live.sqlite")
	db := seedSQLite(t, dbPath, "s8-breakglass")
	_ = db.Close()
	bgOut := filepath.Join(dir, "bg-export.json")
	out.Reset()
	if err := runCutoverWithWriter(t.Context(), []string{
		"break-glass", "--sqlite", dbPath, "--out", bgOut, "--state-file", stateFile2,
	}, &out); err != nil {
		t.Fatalf("break-glass: %v", err)
	}
	var rep cutover.BreakGlassReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || !rep.NoSLACommitment || rep.Tables["brands"] != 1 {
		t.Fatalf("unexpected break-glass report: %s (%v)", out.String(), err)
	}
}
