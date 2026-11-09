package testdb

import (
	"os"
	"strings"
	"testing"
)

// TestTestDatabaseURLGuard pins the DSN guard. A renamed fixture variable
// used to skip every integration test silently, which turned a broken CI
// setup into a green run with no database coverage at all.
//
// The guard is exercised through the environment, never through a live
// cluster, so it runs in the plain unit job as well.
func TestTestDatabaseURLGuard(t *testing.T) {
	for _, name := range append([]string{testDatabaseURLEnv, requireDatabaseURLEnv}, lookalikeDatabaseURLEnvs...) {
		t.Setenv(name, "")
	}
	t.Setenv("CI", "")

	t.Run("configured dsn wins", func(t *testing.T) {
		t.Setenv(testDatabaseURLEnv, "postgres://urmail@127.0.0.1:5432/urmail_fixture?sslmode=disable")
		dsn, err := testDatabaseURL()
		if err != nil || dsn == "" {
			t.Fatalf("dsn=%q err=%v", dsn, err)
		}
	})

	t.Run("local run without a cluster skips", func(t *testing.T) {
		dsn, err := testDatabaseURL()
		if err != nil {
			t.Fatalf("a local run without a cluster must not fail: %v", err)
		}
		if dsn != "" {
			t.Fatalf("dsn=%q, want empty", dsn)
		}
	})

	t.Run("renamed test variable fails", func(t *testing.T) {
		for _, name := range lookalikeDatabaseURLEnvs {
			t.Setenv(name, "")
		}
		t.Setenv(lookalikeDatabaseURLEnvs[0], "postgres://urmail@127.0.0.1:5432/urmail_fixture?sslmode=disable")
		_, err := testDatabaseURL()
		if err == nil {
			t.Fatalf("%s must be reported as a renamed fixture variable", lookalikeDatabaseURLEnvs[0])
		}
		if !strings.Contains(err.Error(), testDatabaseURLEnv) {
			t.Fatalf("error must name the expected variable: %v", err)
		}
	})

	t.Run("required run without a dsn fails", func(t *testing.T) {
		t.Setenv(requireDatabaseURLEnv, "1")
		_, err := testDatabaseURL()
		if err == nil {
			t.Fatal("a run that requires the database must fail without the DSN")
		}
		if !strings.Contains(err.Error(), testDatabaseURLEnv) {
			t.Fatalf("error must name the expected variable: %v", err)
		}
	})

	t.Run("required run with a generic variable fails", func(t *testing.T) {
		t.Setenv(requireDatabaseURLEnv, "1")
		t.Setenv(genericDatabaseURLEnvs[0], "postgres://urmail@127.0.0.1:5432/urmail?sslmode=disable")
		_, err := testDatabaseURL()
		if err == nil {
			t.Fatalf("%s must not be accepted as the fixture DSN", genericDatabaseURLEnvs[0])
		}
	})

	t.Run("generic variable is fine on a local run", func(t *testing.T) {
		t.Setenv(genericDatabaseURLEnvs[0], "postgres://urmail@127.0.0.1:5432/urmail?sslmode=disable")
		dsn, err := testDatabaseURL()
		if err != nil {
			t.Fatalf("a local run must not fail because of %s: %v", genericDatabaseURLEnvs[0], err)
		}
		if dsn != "" {
			t.Fatalf("dsn=%q, want empty", dsn)
		}
	})
}

// TestLookalikeVariablesAreTestScoped pins the lookalike list to
// test-scoped names. A production or development variable such as
// URMAIL_DATABASE_URL is exported on purpose by scripts/dev.sh and the
// installer, so it may never be treated as a renamed fixture variable.
func TestLookalikeVariablesAreTestScoped(t *testing.T) {
	for _, name := range lookalikeDatabaseURLEnvs {
		if !strings.HasPrefix(name, "URMAIL_TEST_") {
			t.Fatalf("%s is not test-scoped and would fail a legitimate local run", name)
		}
	}
	for _, name := range genericDatabaseURLEnvs {
		if strings.HasPrefix(name, "URMAIL_TEST_") {
			t.Fatalf("%s must not be in the generic list: a test-scoped name is always a mistake", name)
		}
	}
}

// TestRequireDatabaseURLEnvNotSetOnADeveloperMachine documents that the
// mandatory-DSN switch is opt-in: an unset switch keeps the local skip.
func TestRequireDatabaseURLEnvNotSetOnADeveloperMachine(t *testing.T) {
	if os.Getenv(requireDatabaseURLEnv) != "" {
		t.Skip("the run opts into the mandatory-DSN rule")
	}
	t.Setenv(testDatabaseURLEnv, "")
	dsn, err := testDatabaseURL()
	if err != nil || dsn != "" {
		t.Fatalf("dsn=%q err=%v", dsn, err)
	}
}
