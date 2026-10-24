package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPostfixV80ProfileExplicitOptInAndEnvParity(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "env")
	t.Setenv("URMAIL_DATABASE_URL", "fixture-app")
	t.Setenv("URMAIL_DATABASE_URL_PLATFORM", "fixture-platform")
	t.Setenv("URMAIL_ENV_FILE", file)
	t.Setenv("URMAIL_POSTFIX_V80_PRESERVE", "")
	t.Setenv("URMAIL_POLICY_ENABLED", "true")
	cfg, err := Load()
	if err != nil || cfg.PostfixV80Preserve {
		t.Fatal("default deployment changed", err)
	}
	for _, name := range []string{"daemon.env", "agent.env"} {
		path := filepath.Join(dir, name)
		if err = os.WriteFile(path, []byte("URMAIL_POSTFIX_V80_PRESERVE=true\nURMAIL_POLICY_ENABLED=true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// An absent environment key permits each process's root-controlled file.
		if err = os.Unsetenv("URMAIL_POSTFIX_V80_PRESERVE"); err != nil {
			t.Fatal(err)
		}
		t.Setenv("URMAIL_ENV_FILE", path)
		cfg, err = Load()
		if err != nil || !cfg.PostfixV80Preserve || !cfg.PolicyEnabled {
			t.Fatal("daemon/agent flag diverged", err)
		}
	}
	t.Setenv("URMAIL_POSTFIX_V80_PRESERVE", "false")
	cfg, err = Load()
	if err != nil || cfg.PostfixV80Preserve {
		t.Fatal("environment override ignored", err)
	}
	t.Setenv("URMAIL_POSTFIX_V80_PRESERVE", "true")
	t.Setenv("URMAIL_POLICY_ENABLED", "false")
	if _, err = Load(); err == nil {
		t.Fatal("preservation permitted disabled SaaS policy")
	}
	t.Setenv("URMAIL_POLICY_ENABLED", "true")
	for _, bad := range []string{"v80", "true\nsmtpd_sender_restrictions=permit", "TRUE,permit", " true "} {
		t.Setenv("URMAIL_POSTFIX_V80_PRESERVE", bad)
		if _, err = Load(); err == nil {
			t.Fatal("invalid profile flag silently disabled preservation")
		}
	}
}
