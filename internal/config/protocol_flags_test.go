package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProtocolFlagsPreserveDefaultsAndRejectInvalid(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want, error bool
	}{
		{"compatible default", "", true, false},
		{"explicit disabled", "false", false, false},
		{"explicit enabled", "true", true, false},
		{"invalid configuration", "invalid", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "protocol.env")
			if err := os.WriteFile(file, []byte("URMAIL_DATABASE_URL=postgres://app@localhost/urmail\nURMAIL_DATABASE_URL_PLATFORM=postgres://platform@localhost/urmail\nURMAIL_QUOTE_HMAC_KEY=fixture-secret\nURMAIL_POLICY_ENABLED=true\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("URMAIL_ENV_FILE", file)
			for _, key := range []string{"URMAIL_CARDDAV_ENABLED", "URMAIL_CALDAV_ENABLED", "URMAIL_EAS_ENABLED"} {
				t.Setenv(key, tc.value)
			}
			cfg, err := Load()
			if tc.error {
				if err == nil || !strings.Contains(err.Error(), "must be a boolean") {
					t.Fatalf("invalid flag error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.CardDAVEnabled != tc.want || cfg.CalDAVEnabled != tc.want || cfg.EASEnabled != tc.want {
				t.Fatal("protocol feature flags ignored")
			}
		})
	}
}
