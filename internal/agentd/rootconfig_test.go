package agentd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootEnvironmentRejectsUnprivilegedAndSymlinkFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.env")
	if err := os.WriteFile(path, []byte("URMAIL_MYHOSTNAME=box.example.com\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRootEnvironment(path); err == nil {
		t.Fatal("unprotected root environment accepted")
	}
	link := filepath.Join(dir, "link.env")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRootEnvironment(link); err == nil {
		t.Fatal("symlink root environment accepted")
	}
}
