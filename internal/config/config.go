// Package config loads urMail.cn runtime configuration.
//
// Configuration comes from two layers: an optional env file
// (/etc/urmail/urmaild.env by default) and the process environment.
// Environment variables always win over the file, so an operator can override
// a single value without editing the file.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// EnvFile is the default location of the on-disk configuration.
const EnvFile = "/etc/urmail/urmaild.env"

// Config is the fully resolved runtime configuration.
type Config struct {
	// ListenAddr is the address the HTTP server binds to. It must stay on
	// loopback: nginx terminates TLS and is the only thing that should be
	// reachable from the network.
	ListenAddr string

	// DatabaseURL is the connection string for the tenant-scoped role
	// (urmail_app). Queries issued over this connection are subject to
	// PostgreSQL row-level security.
	DatabaseURL string

	// DatabaseURLPlatform is the connection string for the platform-scoped
	// role (urmail_platform), which bypasses row-level security. It is only
	// used by platform administration endpoints.
	DatabaseURLPlatform string

	// DatabaseURLMigrate is the connection string for the schema migration
	// role (urmail_migrate). It is a member of urmail_platform so it can
	// transfer table ownership during migrations.
	DatabaseURLMigrate string

	// DatabaseURLDovecot is the connection string for the Dovecot role
	// (urmail_dovecot). Dovecot uses it for passdb/userdb queries across
	// tenants; it has BYPASSRLS but only SELECT on mailboxes/domains.
	DatabaseURLDovecot string

	// MyHostname is the FQDN Postfix announces itself as (myhostname).
	MyHostname string

	// PrimaryDomain is the primary apex domain of the platform (e.g. urmail.cn).
	PrimaryDomain string

	// PolicyEnabled controls whether Postfix queries the policy service.
	PolicyEnabled bool

	// ArchiveEnabled controls whether Postfix routes incoming mail through
	// the archive transport.
	ArchiveEnabled bool

	// StorageRoot holds mailboxes, archives and certificates.
	StorageRoot string

	// AgentSocket is the unix socket of the privileged agent.
	AgentSocket string

	// PolicySocket is the unix socket where the Postfix policy service
	// listens.
	PolicySocket string

	// MasterKeyPath is the file holding the platform master key. It is used
	// to encrypt secrets at rest (DNS provider credentials, payment keys).
	MasterKeyPath string

	// LogLevel is one of debug, info, warn, error.
	LogLevel string

	// Locale is the fallback locale for messages.
	Locale string

	// PublicIPv4 is the public address published in A records and
	// rendered into NSD zone files.
	PublicIPv4 string

	// Nameservers are the NS hosts (FQDN, no trailing dot) published in
	// managed zones.
	Nameservers []string
}

// parseBool returns true if value is "1", "true", "yes", or "on" (case-insensitive).
func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// ErrMissing is returned when a required setting is absent.
var ErrMissing = errors.New("required configuration is missing")

// Load resolves configuration from the environment file and the environment.
func Load() (*Config, error) {
	values, err := loadEnvFile(os.Getenv("URMAIL_ENV_FILE"))
	if err != nil {
		return nil, err
	}
	for _, key := range os.Environ() {
		name, value, found := strings.Cut(key, "=")
		if found {
			values[name] = value
		}
	}

	cfg := &Config{
		ListenAddr:          values["URMAIL_LISTEN_ADDR"],
		DatabaseURL:         values["URMAIL_DATABASE_URL"],
		DatabaseURLPlatform: values["URMAIL_DATABASE_URL_PLATFORM"],
		DatabaseURLMigrate:  values["URMAIL_DATABASE_URL_MIGRATE"],
		DatabaseURLDovecot:  values["URMAIL_DATABASE_URL_DOVECOT"],
		MyHostname:          values["URMAIL_MYHOSTNAME"],
		PrimaryDomain:       values["URMAIL_PRIMARY_DOMAIN"],
		PolicyEnabled:       parseBool(values["URMAIL_POLICY_ENABLED"]),
		ArchiveEnabled:      parseBool(values["URMAIL_ARCHIVE_ENABLED"]),
		StorageRoot:         values["URMAIL_STORAGE_ROOT"],
		AgentSocket:         values["URMAIL_AGENT_SOCKET"],
		PolicySocket:        values["URMAIL_POLICY_SOCKET"],
		MasterKeyPath:       values["URMAIL_MASTER_KEY_PATH"],
		LogLevel:            values["URMAIL_LOG_LEVEL"],
		Locale:              values["URMAIL_LOCALE"],
		PublicIPv4:          values["URMAIL_PUBLIC_IPV4"],
		Nameservers:         splitCSV(values["URMAIL_NAMESERVERS"]),
	}
	cfg.applyDefaults()

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyDefaults fills in the values that have a safe default.
func (c *Config) applyDefaults() {
	if c.ListenAddr == "" {
		c.ListenAddr = "127.0.0.1:8080"
	}
	if c.StorageRoot == "" {
		c.StorageRoot = "/var/mail"
	}
	if c.AgentSocket == "" {
		c.AgentSocket = "/run/urmail/agent.sock"
	}
	if c.PolicySocket == "" {
		c.PolicySocket = "/run/urmail-policy/policy.sock"
	}
	if c.MasterKeyPath == "" {
		c.MasterKeyPath = "/etc/urmail/master.key"
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.Locale == "" {
		c.Locale = "zh-CN"
	}
	if c.PrimaryDomain == "" && c.MyHostname != "" {
		parts := strings.SplitN(c.MyHostname, ".", 2)
		if len(parts) == 2 {
			c.PrimaryDomain = parts[1]
		}
	}
	if c.PublicIPv4 == "" {
		c.PublicIPv4 = "127.0.0.1"
	}
	if len(c.Nameservers) == 0 && c.MyHostname != "" {
		c.Nameservers = []string{c.MyHostname}
	}
	if c.PrimaryDomain == "" {
		c.PrimaryDomain = "urmail.cn"
	}
}

// validate rejects a configuration that cannot work.
//
// The database URLs are required but deliberately not defaulted: silently
// falling back to a local socket would hide a misconfigured install behind a
// confusing runtime error.
func (c *Config) validate() error {
	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "URMAIL_DATABASE_URL")
	}
	if c.DatabaseURLPlatform == "" {
		missing = append(missing, "URMAIL_DATABASE_URL_PLATFORM")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrMissing, strings.Join(missing, ", "))
	}
	return nil
}

// splitCSV splits a comma-separated environment value, dropping blanks.
func splitCSV(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// loadEnvFile reads a KEY=VALUE file. A missing file is not an error:
// the configuration may come entirely from the environment.
func loadEnvFile(path string) (map[string]string, error) {
	values := map[string]string{}
	if path == "" {
		path = EnvFile
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return values, nil
		}
		return nil, fmt.Errorf("open config file %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		values[name] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}
	return values, nil
}

// LoadEnvFile is exported for tests that need to exercise the parser directly.
func LoadEnvFile(path string) (map[string]string, error) {
	return loadEnvFile(path)
}

// ParseBool interprets the usual truthy strings. It is used by installers and
// migration helpers.
func ParseBool(value string) bool {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false
	}
	return parsed
}

// EnsureDir creates a directory with the given permissions.
func EnsureDir(path string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Clean(path), perm); err != nil {
		return fmt.Errorf("create directory %s: %w", path, err)
	}
	return nil
}
