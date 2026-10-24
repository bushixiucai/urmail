package agentd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypedTelemetryRejectsPathsAndArbitraryCommands(t *testing.T) {
	for _, raw := range []string{`{"path":"/etc/shadow"}`, `{"offset":-1}`, `{"identity":"x","offset":0,"command":"id"}`, `{} {}`} {
		if _, _, err := ReadMailLog(json.RawMessage(raw)); err == nil {
			t.Fatalf("unsafe log parameters accepted: %s", raw)
		}
	}
	for _, raw := range []string{`{"command":"id"}`, `{"args":["-d","ALL"]}`, `{} {}`} {
		if _, _, err := ReadPostfixQueue(json.RawMessage(raw)); err == nil {
			t.Fatalf("unsafe queue parameters accepted: %s", raw)
		}
	}
}

func TestLogCursorDrainsRenamedRotationAndDetectsGap(t *testing.T) {
	directory := t.TempDir()
	current := filepath.Join(directory, "mail.log")
	rotated := filepath.Join(directory, "mail.log.1")
	if err := os.WriteFile(current, []byte("first\npartial"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := readMailLogFiles(current, rotated, MailLogReadParams{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Data != "first\n" || first.EndOffset != 6 {
		t.Fatalf("partial tail advanced: %+v", first)
	}
	if err = os.WriteFile(current, []byte("first\npartial-complete\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(current, rotated); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(current, []byte("new-generation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := readMailLogFiles(current, rotated, MailLogReadParams{Identity: first.Identity, Offset: first.EndOffset})
	if err != nil {
		t.Fatal(err)
	}
	if old.Identity != first.Identity || old.Data != "partial-complete\n" || old.Reset {
		t.Fatalf("old rotation not drained: %+v", old)
	}
	next, err := readMailLogFiles(current, rotated, MailLogReadParams{Identity: old.Identity, Offset: old.EndOffset})
	if err != nil {
		t.Fatal(err)
	}
	if !next.Reset || next.GapReason != "" || next.Data != "new-generation\n" {
		t.Fatalf("rotation transition false gap: %+v", next)
	}
	missing, err := readMailLogFiles(current, rotated, MailLogReadParams{Identity: "missing-generation", Offset: 20})
	if err != nil {
		t.Fatal(err)
	}
	if missing.GapReason != "rotation_generation_missing" || !missing.Reset {
		t.Fatalf("missing generation not reported: %+v", missing)
	}
	truncated, err := readMailLogFiles(current, rotated, MailLogReadParams{Identity: next.Identity, Offset: 9999})
	if err != nil {
		t.Fatal(err)
	}
	if truncated.GapReason != "source_truncated" {
		t.Fatalf("truncate not reported: %+v", truncated)
	}
}

func TestMailLogRejectsSymlinkAndOversizedIncompleteLine(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "mail.log")
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := readMailLogFiles(path, path+".1", MailLogReadParams{}); err == nil {
		t.Fatal("followed replaced log symlink")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxTelemetryBytes+10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readMailLogFiles(path, path+".1", MailLogReadParams{}); err == nil {
		t.Fatal("oversized partial log silently advanced")
	}
}

func TestQueueSnapshotIsStructuredAndInvalidOutputFails(t *testing.T) {
	snapshot, err := parseQueueSnapshot([]byte(`{"queue_id":"ABC123","queue_name":"deferred","arrival_time":100,"message_size":12,"sender":"a@test","recipients":[{"address":"b@test","delay_reason":"deferred"}]}`))
	if err != nil || len(snapshot.Entries) != 1 {
		t.Fatalf("queue parse failed: %+v %v", snapshot, err)
	}
	for _, raw := range []string{`not-json`, `{"queue_id":"../../etc","arrival_time":1}`, `{"queue_id":"ABC","message_size":-1}`} {
		if _, err = parseQueueSnapshot([]byte(raw)); err == nil {
			t.Fatalf("invalid queue accepted: %s", raw)
		}
	}
}

func TestMailLogEmptyAndRotatedPartialTailNeverAdvanceHealthy(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "mail.log")
	rotated := current + ".1"
	if err := os.WriteFile(current, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readMailLogFiles(current, rotated, MailLogReadParams{}); err == nil {
		t.Fatal("empty unstable source acquired a checkpoint")
	}
	if err := os.WriteFile(current, []byte("first\npartial"), 0o600); err != nil {
		t.Fatal(err)
	}
	batch, err := readMailLogFiles(current, rotated, MailLogReadParams{})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(current, rotated); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(current, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readMailLogFiles(current, rotated, MailLogReadParams{Identity: batch.Identity, Offset: batch.EndOffset}); err == nil {
		t.Fatal("rotated partial tail silently stuck on a healthy cursor")
	}
}
