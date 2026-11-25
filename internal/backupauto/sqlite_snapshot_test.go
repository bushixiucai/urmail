package backupauto

import (
	"strings"
	"testing"
)

// TestCompleteLocalArtifactsAcceptsSQLiteSnapshot is the QA-named gate: a
// single-engine snapshot must be classified as a complete backup, exactly like
// the PostgreSQL dump, or the fence pre-flight rejects every SQLite backup.
func TestCompleteLocalArtifactsAcceptsSQLiteSnapshot(t *testing.T) {
	stamp := "20260101T000000Z"
	hash := strings.Repeat("a", 64)
	artifacts := []LocalArtifact{
		{Name: "urmail-" + stamp + ".sqlite", Kind: KindSqliteSnapshot, Size: 4096, SHA256: hash},
		{Name: "urmail-config-" + stamp + ".tar.gz", Kind: KindConfigTar, Size: 512, SHA256: hash},
		{Name: "urmail-mail-" + stamp + ".tar.gz", Kind: KindMailTar, Size: 512, SHA256: hash},
	}
	if err := completeLocalArtifacts(stamp, artifacts); err != nil {
		t.Fatalf("sqlite snapshot rejected as incomplete: %v", err)
	}

	wrongKind := append([]LocalArtifact(nil), artifacts...)
	wrongKind[0].Kind = KindPgDump
	if err := completeLocalArtifacts(stamp, wrongKind); err == nil {
		t.Fatal("sqlite snapshot recorded as pg_dump was accepted")
	}

	missingDB := append([]LocalArtifact(nil), artifacts[1:]...)
	if err := completeLocalArtifacts(stamp, missingDB); err == nil {
		t.Fatal("backup without a database artifact was accepted")
	}

	bothKinds := append([]LocalArtifact(nil), artifacts...)
	bothKinds = append(bothKinds, LocalArtifact{Name: "urmail-" + stamp + ".dump", Kind: KindPgDump, Size: 1, SHA256: hash})
	if err := completeLocalArtifacts(stamp, bothKinds); err == nil {
		t.Fatal("backup with two database artifacts was accepted")
	}
}

func TestPlanBackupSetSQLiteChoosesSnapshot(t *testing.T) {
	entries, err := PlanBackupSet(PlannerConfig{OutDir: "/var/backups/urmail", Stamp: "20260101T000000Z", IncludeMail: true, SQLite: true})
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Kind != KindSqliteSnapshot || entries[0].Name != "urmail-20260101T000000Z.sqlite" {
		t.Fatalf("sqlite plan database entry = %+v", entries[0])
	}
	pg, err := PlanBackupSet(PlannerConfig{OutDir: "/var/backups/urmail", Stamp: "20260101T000000Z", IncludeMail: true})
	if err != nil {
		t.Fatal(err)
	}
	if pg[0].Kind != KindPgDump {
		t.Fatalf("default plan database entry = %+v", pg[0])
	}
}
