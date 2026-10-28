package agentd

import (
	"encoding/json"
	"strings"
	"testing"
)

// OPS-001: backup.local.create takes NO parameters. Every attempt to
// influence the command (output directory, extra arguments, scope) must be
// rejected before any process is spawned.
func TestBackupLocalCreateRejectsAnyParameters(t *testing.T) {
	for name, raw := range map[string]string{
		"out-dir override": `{"out_dir":"/tmp"}`,
		"command suffix":   `{"args":["--no-maintenance"]}`,
		"scope change":     `{"include_mail":false}`,
		"array payload":    `["x"]`,
		"string payload":   `"backup"`,
	} {
		_, _, err := BackupLocalCreate(json.RawMessage(raw))
		if err == nil {
			t.Fatalf("%s: parameters must be rejected", name)
		}
		if !strings.Contains(err.Error(), "rejected") {
			t.Fatalf("%s: rejection must be classified ErrRejected, got %v", name, err)
		}
	}
}

func TestBackupLocalCreateAcceptsEmptyAndNull(t *testing.T) {
	// These must fail at EXECUTION time (binary absent on dev hosts), not
	// at validation time; the error must not be a parameter rejection.
	for _, raw := range []string{"{}", "null", ""} {
		_, _, err := BackupLocalCreate(json.RawMessage(raw))
		if err != nil && strings.Contains(err.Error(), "rejected") {
			t.Fatalf("empty payload %q must not be rejected as parameters: %v", raw, err)
		}
	}
}

func TestBackupLocalCreateCommandIsFixed(t *testing.T) {
	if strings.Join(backupLocalCreateCommand, " ") != "/usr/bin/systemctl start --no-block urmail-backup-auto.service" {
		t.Fatal("backup must queue only the fixed independent unit")
	}
}

// OPS-008: the backup spool is NOT an agent write root. Artifacts are
// produced exclusively by the fixed root command above; broadening
// file.write to /var/backups would let a compromised urmaild plant or
// replace spool files that restore and the offsite upload then trust. The
// boundary is asserted on both the spool itself and a sibling directory.
func TestFileWriteCannotTouchBackupSpool(t *testing.T) {
	for _, root := range allowedWriteRoots {
		if root == "/var/backups" || strings.HasPrefix(root, "/var/backups/") {
			t.Fatalf("backup spool must not be an agent write root, found %q", root)
		}
	}
	for name, path := range map[string]string{
		"spool artifact":   "/var/backups/urmail/urmail-20260101T000000Z.dump",
		"sibling root":     "/var/backups/other/replace-me",
		"root itself":      "/var/backups",
		"traversal escape": "/etc/urmail/../../var/backups/urmail/urmail-20260101T000000Z.dump",
	} {
		_, _, err := WriteFile([]byte(`{"path":` + mustJSONString(path) + `,"content":"x"}`))
		if err == nil || !strings.Contains(err.Error(), "rejected") {
			t.Fatalf("%s: file.write must be rejected, got %v", name, err)
		}
	}
}

func mustJSONString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
