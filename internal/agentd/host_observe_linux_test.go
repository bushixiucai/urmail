//go:build linux

package agentd

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
)

func TestHostObservationLinuxFixedSources(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("runs only in isolated root Linux acceptance container")
	}
	for _, path := range []string{"/var/mail", "/var/lib/urmail"} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manager := &ApplyManager{cfg: &config.Config{MyHostname: "fixture.example.test"}, root: t.TempDir()}
	encoded, _, err := manager.ObserveHost([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var value HostObservation
	if err = json.Unmarshal([]byte(encoded), &value); err != nil {
		t.Fatal(err)
	}
	if value.CPUTotal == 0 || value.MemoryTotal == 0 || len(value.Filesystems) != 2 || len(value.Daemons) != 5 || !value.CertificateUnavailable {
		t.Fatalf("invalid Linux observation: %+v", value)
	}
	if value.Daemons["postfix"] {
		t.Fatal("a non-systemd isolated container claimed the daemon was active")
	}
}
