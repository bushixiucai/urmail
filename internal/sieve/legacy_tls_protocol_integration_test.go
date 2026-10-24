package sieve

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/dovecot"
)

// This separate fixture retains the frozen raw-protocol test and verifies the
// same storage/query behavior with the deployment's required STARTTLS policy.
func TestLegacyRawManageSieveWithStrictSTARTTLS(t *testing.T) {
	image := os.Getenv("URMAIL_SIEVE_TEST_IMAGE")
	if image == "" {
		t.Skip("URMAIL_SIEVE_TEST_IMAGE is not configured")
	}
	base, err := filepath.Abs("testdata/legacy-protocol.py")
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := filepath.Abs("testdata/legacy-tls-protocol.py")
	if err != nil {
		t.Fatal(err)
	}
	files, err := dovecot.Generate(dovecot.NodeConfig{Hostname: "box.example.test", PrimaryDomain: "example.test", StorageRoot: "/tmp/source-root", TLSCertFile: "/tmp/fixture.cert", TLSKeyFile: "/tmp/fixture.key", DatabaseURL: "host=/tmp/legacy-pg-socket dbname=legacy_sieve user=postgres sslmode=disable", MailUID: "65534", MailGID: "65534"})
	if err != nil {
		t.Fatal(err)
	}
	dbConf := filepath.Join(t.TempDir(), "db.conf")
	if err = os.WriteFile(dbConf, []byte(files[dovecot.FileDBConf]), 0644); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "docker", "run", "--rm", "--network", "none", "--entrypoint", "/usr/bin/python3", "-v", base+":/test-base.py:ro", "-v", wrapper+":/test.py:ro", "-v", dbConf+":/test-db.conf:ro", image, "-I", "-B", "/test.py")
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("isolated strict STARTTLS failed: %v", err)
	}
}
