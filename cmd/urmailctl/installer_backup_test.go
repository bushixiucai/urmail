package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepeatInstallerPreservesOnlyRootBackupSettings(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	raw, err := os.ReadFile("../../installer/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(raw), "python3 - /etc/urmail/agent.env")
	if start < 0 {
		t.Fatal("root configuration merge missing")
	}
	script := strings.SplitN(string(raw)[start:], "<<'PYCODE'\n", 2)
	if len(script) != 2 {
		t.Fatal("root environment merge missing")
	}
	source := strings.SplitN(script[1], "\nPYCODE", 2)[0]
	directory := t.TempDir()
	current, previous := filepath.Join(directory, "agent.env"), filepath.Join(directory, "previous.env")
	old := "URMAIL_BACKUP_S3_SECRET_KEY=synthetic-preserved-secret\nURMAIL_BACKUP_AGE_RECIPIENT=synthetic-age\nURMAIL_BACKUP_KEEP_DAILY=9\nURMAIL_BACKUP_POSTGRES_CONTAINER=owned-pg\nUNRELATED_OLD_KEY=must-not-copy\n"
	if err = os.WriteFile(previous, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(current, []byte("BASE=current\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), python, "-", current, previous, "", "", "")
	command.Stdin = strings.NewReader(source)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "URMAIL_BACKUP_KEEP_DAILY=11"}
	if output, e := command.CombinedOutput(); e != nil {
		t.Fatalf("repeat root configuration merge: %v %s", e, output)
	}
	result, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	text := string(result)
	if !strings.Contains(text, "URMAIL_BACKUP_S3_SECRET_KEY=synthetic-preserved-secret\n") || !strings.Contains(text, "URMAIL_BACKUP_AGE_RECIPIENT=synthetic-age\n") || !strings.Contains(text, "URMAIL_BACKUP_KEEP_DAILY=11\n") || !strings.Contains(text, "URMAIL_BACKUP_POSTGRES_CONTAINER=owned-pg\n") || strings.Contains(text, "UNRELATED_OLD_KEY") {
		t.Fatal("repeat installation lost root-only backup configuration or ignored explicit override")
	}
	info, err := os.Stat(current)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("root configuration exposed", err)
	}
}
