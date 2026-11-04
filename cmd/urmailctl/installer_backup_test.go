package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepeatInstallerPreservesRootBackupSettings(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	raw, err := os.ReadFile("../../installer/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "python3 /usr/share/urmail/configure-agent-env.py") {
		t.Fatal("root configuration merge missing")
	}
	directory := t.TempDir()
	current, daemon := filepath.Join(directory, "agent.env"), filepath.Join(directory, "daemon.env")
	old := "URMAIL_BACKUP_S3_SECRET_KEY=synthetic-preserved-secret\nURMAIL_BACKUP_AGE_RECIPIENT=synthetic-age\nURMAIL_BACKUP_KEEP_DAILY=9\nURMAIL_BACKUP_POSTGRES_CONTAINER=owned-pg\nURMAIL_ROOT_UNKNOWN_KEY=must-preserve\n"
	if err = os.WriteFile(current, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	defaults := "URMAIL_DATABASE_URL=app\nURMAIL_DATABASE_URL_PLATFORM=platform\nURMAIL_DATABASE_URL_MIGRATE=migrate\nURMAIL_DATABASE_URL_DOVECOT=dovecot\nURMAIL_POLICY_ENABLED=true\n"
	if err = os.WriteFile(daemon, []byte(defaults), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), python, "../../installer/configure-agent-env.py", daemon, current)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "URMAIL_BACKUP_KEEP_DAILY=11"}
	if output, e := command.CombinedOutput(); e != nil {
		t.Fatalf("repeat root configuration merge: %v %s", e, output)
	}
	result, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	text := string(result)
	if !strings.Contains(text, "URMAIL_BACKUP_S3_SECRET_KEY=synthetic-preserved-secret\n") || !strings.Contains(text, "URMAIL_BACKUP_AGE_RECIPIENT=synthetic-age\n") || !strings.Contains(text, "URMAIL_BACKUP_KEEP_DAILY=11\n") || !strings.Contains(text, "URMAIL_BACKUP_POSTGRES_CONTAINER=owned-pg\n") || !strings.Contains(text, "URMAIL_ROOT_UNKNOWN_KEY=must-preserve\n") {
		t.Fatal("repeat installation lost root-only backup configuration or ignored explicit override")
	}
	info, err := os.Stat(current)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("root configuration exposed", err)
	}
}
