package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDNSSecondaryOnlyIsExplicitTypedAndDoesNotRequireMailDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secondary.env")
	if err := os.WriteFile(path, []byte("URMAIL_DNS_SECONDARY_ONLY=true\nURMAIL_MYHOSTNAME=ns2.example.test\nURMAIL_DNS_LISTEN_ADDRESSES=127.0.0.1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("URMAIL_ENV_FILE", path)
	t.Setenv("URMAIL_DATABASE_URL", "")
	t.Setenv("URMAIL_DATABASE_URL_PLATFORM", "")
	t.Setenv("URMAIL_POLICY_ENABLED", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DNSSecondaryOnly {
		t.Fatal("explicit secondary mode missing")
	}
	t.Setenv("URMAIL_DNS_SECONDARY_ONLY", "false")
	if _, err = Load(); err == nil {
		t.Fatal("normal mail mode bypassed required database and policy")
	}
	t.Setenv("URMAIL_DNS_SECONDARY_ONLY", "typo")
	if _, err = Load(); err == nil {
		t.Fatal("mistyped secondary mode accepted")
	}
}
