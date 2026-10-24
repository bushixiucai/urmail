package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRequiresDatabaseURLs(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "urmaild.env")
	if err := os.WriteFile(envFile, []byte("URMAIL_LISTEN_ADDR=127.0.0.1:9000\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	t.Setenv("URMAIL_ENV_FILE", envFile)
	t.Setenv("URMAIL_DATABASE_URL", "")
	t.Setenv("URMAIL_DATABASE_URL_PLATFORM", "")

	_, err := Load()
	if err == nil {
		t.Fatal("a configuration without database URLs must fail")
	}
	if !strings.Contains(err.Error(), "URMAIL_DATABASE_URL") {
		t.Fatalf("the error should name the missing variables, got %v", err)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "urmaild.env")
	content := "URMAIL_DATABASE_URL=postgres://app@localhost/urmail\n" +
		"URMAIL_DATABASE_URL_PLATFORM=postgres://platform@localhost/urmail\n" +
		"URMAIL_QUOTE_HMAC_KEY=quote-secret-from-file\n"
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	t.Setenv("URMAIL_ENV_FILE", envFile)
	t.Setenv("URMAIL_STORAGE_ROOT", "")
	t.Setenv("URMAIL_AGENT_SOCKET", "")
	t.Setenv("URMAIL_LISTEN_ADDR", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("unexpected listen address %q", cfg.ListenAddr)
	}
	if cfg.StorageRoot != "/var/mail" {
		t.Fatalf("unexpected storage root %q", cfg.StorageRoot)
	}
	if cfg.AgentSocket != "/run/urmail/agent.sock" {
		t.Fatalf("unexpected agent socket %q", cfg.AgentSocket)
	}
	if cfg.Locale != "zh-CN" {
		t.Fatalf("unexpected locale %q", cfg.Locale)
	}
	if cfg.QuoteHMACKey != "quote-secret-from-file" {
		t.Fatalf("quote HMAC key was not loaded, got %q", cfg.QuoteHMACKey)
	}
}

func TestEnvironmentOverridesTheFile(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "urmaild.env")
	content := "URMAIL_DATABASE_URL=postgres://app@localhost/urmail\n" +
		"URMAIL_DATABASE_URL_PLATFORM=postgres://platform@localhost/urmail\n" +
		"URMAIL_QUOTE_HMAC_KEY=quote-secret-from-file\n" +
		"URMAIL_LISTEN_ADDR=127.0.0.1:1111\n"
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	t.Setenv("URMAIL_ENV_FILE", envFile)
	t.Setenv("URMAIL_LISTEN_ADDR", "127.0.0.1:2222")
	t.Setenv("URMAIL_QUOTE_HMAC_KEY", "quote-secret-from-env")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:2222" {
		t.Fatalf("the environment must win, got %q", cfg.ListenAddr)
	}
	if cfg.QuoteHMACKey != "quote-secret-from-env" {
		t.Fatalf("the environment should override the quote key file value")
	}
}

func TestLoadEnvFileHandlesCommentsAndQuotes(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "urmaild.env")
	content := "# a comment\n" +
		"\n" +
		"export QUOTED=\"a value\"\n" +
		"SINGLE='another value'\n" +
		"MALFORMED\n"
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	values, err := LoadEnvFile(envFile)
	if err != nil {
		t.Fatalf("load env file: %v", err)
	}

	if values["QUOTED"] != "a value" {
		t.Fatalf("unexpected QUOTED value %q", values["QUOTED"])
	}
	if values["SINGLE"] != "another value" {
		t.Fatalf("unexpected SINGLE value %q", values["SINGLE"])
	}
	if _, ok := values["MALFORMED"]; ok {
		t.Fatal("a malformed line must be skipped")
	}
}

func TestMissingEnvFileIsNotAnError(t *testing.T) {
	t.Setenv("URMAIL_ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv("URMAIL_DATABASE_URL", "postgres://app@localhost/urmail")
	t.Setenv("URMAIL_DATABASE_URL_PLATFORM", "postgres://platform@localhost/urmail")

	if _, err := Load(); err != nil {
		t.Fatalf("a missing env file must not fail the load: %v", err)
	}
}
func TestLoadMailConfig(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "urmaild.env")
	content := "URMAIL_DATABASE_URL=postgres://app@localhost/urmail\n" +
		"URMAIL_DATABASE_URL_PLATFORM=postgres://platform@localhost/urmail\n" +
		"URMAIL_MYHOSTNAME=mail.example.com\n" +
		"URMAIL_POLICY_ENABLED=true\n" +
		"URMAIL_ARCHIVE_ENABLED=1\n"
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	t.Setenv("URMAIL_ENV_FILE", envFile)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.MyHostname != "mail.example.com" {
		t.Errorf("expected MyHostname 'mail.example.com', got %q", cfg.MyHostname)
	}
	if !cfg.PolicyEnabled {
		t.Errorf("expected PolicyEnabled true, got false")
	}
	if !cfg.ArchiveEnabled {
		t.Errorf("expected ArchiveEnabled true, got false")
	}
}

func TestDevelopmentModeRequiresLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8080", "[::]:8080", "example.com:8080"} {
		cfg := Config{DevMode: true, ListenAddr: addr, DatabaseURL: "app", DatabaseURLPlatform: "platform"}
		if err := cfg.validate(); err == nil {
			t.Fatalf("accepted public development listener %s", addr)
		}
	}
	cfg := Config{DevMode: true, ListenAddr: "127.0.0.1:8080", DatabaseURL: "app", DatabaseURLPlatform: "platform"}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDNSListenersExcludeWildcardResolverStubAndDeduplicate(t *testing.T) {
	for _, input := range []string{"0.0.0.0", "::", "127.0.0.53", "127.0.0.54", "192.0.2.1:53", "fe80::1%eth0", "224.0.0.1", "192.0.2.1\nserver: evil"} {
		if _, err := resolveDNSListenAddresses(input); err == nil {
			t.Errorf("unsafe DNS override accepted %q", input)
		}
	}
	addresses, err := resolveDNSListenAddresses("192.168.1.2,127.0.0.1,2001:db8::1,192.168.1.2")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(addresses, ",") != "127.0.0.1,192.168.1.2,2001:db8::1" {
		t.Fatalf("listeners %v", addresses)
	}
	automatic, err := resolveDNSListenAddresses("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(automatic, ","), "127.0.0.1") {
		t.Fatal("automatic loopback missing")
	}
	for _, address := range automatic {
		if address == "0.0.0.0" || address == "::" || address == "127.0.0.53" || address == "127.0.0.54" {
			t.Fatalf("automatic unsafe listener %s", address)
		}
	}
}
