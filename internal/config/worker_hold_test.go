package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkersHeldIsTypedAndEnvironmentOverridesDisk(t *testing.T) {
	env := filepath.Join(t.TempDir(), "urmail.env")
	if err := os.WriteFile(env, []byte("URMAIL_DATABASE_URL=postgres://app@localhost/urmail\nURMAIL_DATABASE_URL_PLATFORM=postgres://platform@localhost/urmail\nURMAIL_QUOTE_HMAC_KEY=test-key\nURMAIL_POLICY_ENABLED=true\nURMAIL_WORKERS_HELD=false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("URMAIL_ENV_FILE", env)
	for _, value := range []string{"", "false", "true", "1", "0", "mistyped"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("URMAIL_WORKERS_HELD", value)
			cfg, err := Load()
			if value == "mistyped" {
				if err == nil {
					t.Fatal("unsafe misspelling silently opened workers")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			held := value == "true" || value == "1"
			if cfg.WorkersHeld != held {
				t.Fatalf("WorkersHeld=%v", cfg.WorkersHeld)
			}
			if cfg.PolicyEnabled != true {
				t.Fatal("worker hold disabled SMTP enforcement")
			}
			if cfg.BackgroundWorkersAllowed() == held {
				t.Fatal("side-effect startup policy mismatch")
			}
		})
	}
}
