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
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// EnvFile is the default location of the on-disk configuration.
const EnvFile = "/etc/urmail/urmaild.env"

// Config is the fully resolved runtime configuration.
type Config struct {
	DatabaseBudgets      DatabaseBudgets
	DNSSecondaryOnly     bool
	DNSListenAddresses   []string
	DevMode              bool
	MarketingEnabled     bool
	NotificationsEnabled bool
	RefundsEnabled       bool
	Quarantined          bool
	WorkersHeld          bool
	ACMEServer           string
	ACMERootCA           string
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

	// PostfixV80Preserve enables the fixed, reviewed v80 SMTP compatibility profile.
	// It requires the SaaS policy service and never accepts raw restrictions.
	PostfixV80Preserve bool

	// PreserveExternalNSD keeps an existing authoritative NSD tree untouched.
	// Used for v80 same-box mail cutover while source zones/DNSSEC remain live.
	PreserveExternalNSD bool

	// PreserveExternalWeb keeps existing nginx site/TLS layout untouched.
	PreserveExternalWeb bool

	// PreserveExternalOpenDKIM keeps the live OpenDKIM milter/config untouched
	// so Postfix can keep signing through the source milter without dual-sign.
	PreserveExternalOpenDKIM bool

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

	// DNSProvider enables automatic DNS record writes for delegated domains.
	// Supported: "cloudflare" (currently).
	DNSProvider string

	// CloudflareToken is the API token used when DNSProvider is cloudflare.
	CloudflareToken string

	// DNSPodToken is the "id,token" pair used when DNSProvider is dnspod.
	DNSPodToken string

	// AliyunAccessKeyID/AliyunAccessKeySecret are used when DNSProvider is aliyun.
	AliyunAccessKeyID     string
	AliyunAccessKeySecret string

	// DNSManagedZones lists the DNS zones the provider credentials may write.
	// A domain is only auto-verified through the provider when it falls inside
	// one of these zones; anything else must verify manually. Without this
	// allowlist a tenant could aim the platform credentials at an arbitrary
	// zone before ownership verification (M5 review 3.11).
	DNSManagedZones []string

	// StripeSecretKey enables Stripe Checkout when set.
	StripeSecretKey string

	// StripeWebhookSecret verifies /billing/webhook/stripe.
	StripeWebhookSecret string

	// QuoteHMACKey signs billing quote snapshots; it has no insecure default.
	QuoteHMACKey string
	// MarketingUnsubscribeHMACKey signs recipient opt-out tokens; no default.
	MarketingUnsubscribeHMACKey string
	RoundcubeBridgeKey          string
	RoundcubeEnabled            bool
	// WebmailNative enables the native IMAP-proxy webmail API under
	// /api/v1/mailbox/... . It is off by default and independent of Roundcube.
	WebmailNative bool
	// Protocol flags default to the existing enabled behavior. Explicit false
	// disables discovery and every transport alias without disabling the UI.
	CardDAVEnabled bool
	CalDAVEnabled  bool
	EASEnabled     bool
	// IMAPAddr is the loopback IMAP endpoint urmaild dials as the webmail proxy.
	// Used only when WebmailNative is true.
	IMAPAddr         string
	MetricsToken     string
	AICloudEndpoints []string
	AILocalEndpoints []string
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

	dnsSecondaryOnly := false
	if raw := values["URMAIL_DNS_SECONDARY_ONLY"]; raw != "" {
		dnsSecondaryOnly, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, errors.New("URMAIL_DNS_SECONDARY_ONLY must be a boolean")
		}
	}
	workersHeld := false
	if raw := values["URMAIL_WORKERS_HELD"]; raw != "" {
		workersHeld, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, errors.New("URMAIL_WORKERS_HELD must be a boolean")
		}
	}
	var preserveV80 bool
	if raw := values["URMAIL_POSTFIX_V80_PRESERVE"]; raw != "" {
		preserveV80, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, errors.New("URMAIL_POSTFIX_V80_PRESERVE must be a boolean")
		}
	}
	parsePreserve := func(key string) (bool, error) {
		raw := values[key]
		if raw == "" {
			return false, nil
		}
		value, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return false, fmt.Errorf("%s must be a boolean", key)
		}
		return value, nil
	}
	preserveNSD, err := parsePreserve("URMAIL_PRESERVE_EXTERNAL_NSD")
	if err != nil {
		return nil, err
	}
	preserveWeb, err := parsePreserve("URMAIL_PRESERVE_EXTERNAL_WEB")
	if err != nil {
		return nil, err
	}
	preserveDKIM, err := parsePreserve("URMAIL_PRESERVE_EXTERNAL_OPENDKIM")
	if err != nil {
		return nil, err
	}
	protocolFlag := func(key string) (bool, error) {
		raw := strings.TrimSpace(values[key])
		if raw == "" {
			return true, nil
		}
		value, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return false, fmt.Errorf("%s must be a boolean", key)
		}
		return value, nil
	}
	cardDAVEnabled, err := protocolFlag("URMAIL_CARDDAV_ENABLED")
	if err != nil {
		return nil, err
	}
	calDAVEnabled, err := protocolFlag("URMAIL_CALDAV_ENABLED")
	if err != nil {
		return nil, err
	}
	easEnabled, err := protocolFlag("URMAIL_EAS_ENABLED")
	if err != nil {
		return nil, err
	}
	budgets, err := loadDatabaseBudgets(values)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		DatabaseBudgets:             budgets,
		DevMode:                     parseBool(values["URMAIL_DEV_MODE"]),
		WorkersHeld:                 workersHeld,
		DNSSecondaryOnly:            dnsSecondaryOnly,
		MarketingEnabled:            parseBool(values["URMAIL_MARKETING_ENABLED"]),
		NotificationsEnabled:        parseBool(values["URMAIL_NOTIFICATIONS_ENABLED"]),
		RefundsEnabled:              parseBool(values["URMAIL_REFUNDS_ENABLED"]),
		Quarantined:                 parseBool(values["URMAIL_QUARANTINED"]) || restoreQuarantinePresent(RestoreQuarantineMarker),
		ACMEServer:                  values["URMAIL_ACME_SERVER"],
		ACMERootCA:                  values["URMAIL_ACME_ROOT_CA"],
		ListenAddr:                  values["URMAIL_LISTEN_ADDR"],
		DatabaseURL:                 values["URMAIL_DATABASE_URL"],
		DatabaseURLPlatform:         values["URMAIL_DATABASE_URL_PLATFORM"],
		DatabaseURLMigrate:          values["URMAIL_DATABASE_URL_MIGRATE"],
		DatabaseURLDovecot:          values["URMAIL_DATABASE_URL_DOVECOT"],
		MyHostname:                  values["URMAIL_MYHOSTNAME"],
		PrimaryDomain:               values["URMAIL_PRIMARY_DOMAIN"],
		PolicyEnabled:               parseBool(values["URMAIL_POLICY_ENABLED"]),
		PostfixV80Preserve:          preserveV80,
		PreserveExternalNSD:         preserveNSD,
		PreserveExternalWeb:         preserveWeb,
		PreserveExternalOpenDKIM:    preserveDKIM,
		ArchiveEnabled:              parseBool(values["URMAIL_ARCHIVE_ENABLED"]),
		StorageRoot:                 values["URMAIL_STORAGE_ROOT"],
		AgentSocket:                 values["URMAIL_AGENT_SOCKET"],
		PolicySocket:                values["URMAIL_POLICY_SOCKET"],
		MasterKeyPath:               values["URMAIL_MASTER_KEY_PATH"],
		LogLevel:                    values["URMAIL_LOG_LEVEL"],
		Locale:                      values["URMAIL_LOCALE"],
		PublicIPv4:                  values["URMAIL_PUBLIC_IPV4"],
		Nameservers:                 splitCSV(values["URMAIL_NAMESERVERS"]),
		DNSProvider:                 strings.ToLower(strings.TrimSpace(values["URMAIL_DNS_PROVIDER"])),
		DNSManagedZones:             splitCSV(values["URMAIL_DNS_MANAGED_ZONES"]),
		CloudflareToken:             values["URMAIL_CLOUDFLARE_TOKEN"],
		DNSPodToken:                 values["URMAIL_DNSPOD_TOKEN"],
		AliyunAccessKeyID:           values["URMAIL_ALIYUN_ACCESS_KEY_ID"],
		AliyunAccessKeySecret:       values["URMAIL_ALIYUN_ACCESS_KEY_SECRET"],
		StripeSecretKey:             values["URMAIL_STRIPE_SECRET_KEY"],
		StripeWebhookSecret:         values["URMAIL_STRIPE_WEBHOOK_SECRET"],
		QuoteHMACKey:                values["URMAIL_QUOTE_HMAC_KEY"],
		MarketingUnsubscribeHMACKey: values["URMAIL_MARKETING_UNSUBSCRIBE_HMAC_KEY"],
		RoundcubeBridgeKey:          values["URMAIL_ROUNDCUBE_BRIDGE_KEY"],
		RoundcubeEnabled:            parseBool(values["URMAIL_ROUNDCUBE_ENABLED"]),
		WebmailNative:               parseBool(values["URMAIL_WEBMAIL_NATIVE"]),
		CardDAVEnabled:              cardDAVEnabled,
		CalDAVEnabled:               calDAVEnabled,
		EASEnabled:                  easEnabled,
		IMAPAddr:                    values["URMAIL_IMAP_ADDR"],
		MetricsToken:                values["URMAIL_METRICS_TOKEN"],
		AICloudEndpoints:            splitCSV(values["URMAIL_AI_CLOUD_ENDPOINTS"]),
		AILocalEndpoints:            splitCSV(values["URMAIL_AI_LOCAL_ENDPOINTS"]),
	}
	cfg.applyDefaults()
	cfg.DNSListenAddresses, err = resolveDNSListenAddresses(values["URMAIL_DNS_LISTEN_ADDRESSES"])
	if err != nil {
		return nil, err
	}

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
	if c.IMAPAddr == "" {
		// Production Dovecot disables cleartext 143; prefer IMAPS loopback.
		c.IMAPAddr = "127.0.0.1:993"
	}
	if len(c.AICloudEndpoints) == 0 {
		c.AICloudEndpoints = []string{
			"https://api.openai.com/v1",
			"https://api.deepseek.com/v1",
			"https://api.deepseek.com",
			"https://dashscope.aliyuncs.com/compatible-mode/v1",
			"https://api.moonshot.cn/v1",
			"https://api.siliconflow.cn/v1",
			"https://open.bigmodel.cn/api/paas/v4",
		}
	}
	if len(c.AILocalEndpoints) == 0 {
		c.AILocalEndpoints = []string{
			"http://127.0.0.1:11434",
			"http://localhost:11434",
		}
	}
}

// validate rejects a configuration that cannot work.
//
// The database URLs are required but deliberately not defaulted: silently
// falling back to a local socket would hide a misconfigured install behind a
// confusing runtime error.
func (c *Config) validate() error {
	if c.DNSSecondaryOnly {
		if c.MyHostname == "" || len(c.DNSListenAddresses) == 0 {
			return errors.New("DNS-only secondary requires an explicit hostname and listen addresses")
		}
		for _, address := range c.DNSListenAddresses {
			ip := net.ParseIP(address)
			if ip == nil || ip.IsUnspecified() {
				return errors.New("DNS-only secondary requires concrete listen IPs")
			}
		}
		if c.PreserveExternalNSD {
			return errors.New("DNS-only secondary cannot manage preserved external NSD")
		}
		return nil
	}

	if c.PostfixV80Preserve && !c.PolicyEnabled {
		return errors.New("v80 SMTP preservation requires URMAIL_POLICY_ENABLED")
	}
	host, port, err := net.SplitHostPort(c.ListenAddr)
	listenerPort, portErr := strconv.Atoi(port)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || portErr != nil || listenerPort < 1 || listenerPort > 65535 {
		return errors.New("HTTP requires an explicit loopback IP and a port between 1 and 65535")
	}
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
	// CFG-01: outside development the SaaS policy service is the only place
	// subscription eligibility and daily send accounting are enforced, so a
	// deployment must not silently boot without it.
	if !c.DevMode && !c.PolicyEnabled {
		return errors.New("production requires URMAIL_POLICY_ENABLED=true: without the policy service, send eligibility and daily accounting are not enforced")
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
	file, err := os.Open(path) // #nosec G304 G703 -- operator-selected configuration file, never request data.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return values, nil
		}
		return nil, fmt.Errorf("open config file %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

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

// resolveDNSListenAddresses binds concrete interfaces without taking over the
// system resolver's loopback stub or assuming a NAT public IP is local.
func resolveDNSListenAddresses(override string) ([]string, error) {
	var ips []net.IP
	if override != "" {
		entries := splitCSV(override)
		if len(entries) == 0 || len(entries) > 64 {
			return nil, fmt.Errorf("invalid URMAIL_DNS_LISTEN_ADDRESSES")
		}
		for _, entry := range entries {
			ip := net.ParseIP(entry)
			if ip == nil || (!ip.IsGlobalUnicast() && !ip.Equal(net.ParseIP("127.0.0.1")) && !ip.Equal(net.IPv6loopback)) {
				return nil, fmt.Errorf("invalid URMAIL_DNS_LISTEN_ADDRESSES")
			}
			ips = append(ips, ip)
		}
	} else {
		ips = append(ips, net.ParseIP("127.0.0.1"))
		interfaces, err := net.Interfaces()
		if err != nil {
			return nil, fmt.Errorf("enumerate DNS listen interfaces: %w", err)
		}
		for _, iface := range interfaces {
			if iface.Flags&net.FlagUp == 0 {
				continue
			}
			addresses, err := iface.Addrs()
			if err != nil {
				return nil, fmt.Errorf("enumerate DNS listen addresses: %w", err)
			}
			for _, address := range addresses {
				ip, _, parseErr := net.ParseCIDR(address.String())
				if parseErr == nil && ip.IsGlobalUnicast() {
					ips = append(ips, ip)
				}
			}
		}
	}
	unique := map[string]bool{}
	for _, ip := range ips {
		unique[ip.String()] = true
	}
	result := make([]string, 0, len(unique))
	for ip := range unique {
		result = append(result, ip)
	}
	sort.Strings(result)
	return result, nil
}

// BackgroundWorkersAllowed excludes maintenance/restore/development modes
// without disabling mail protocols, policy enforcement or config convergence.
func (c *Config) BackgroundWorkersAllowed() bool {
	return c != nil && !c.DevMode && !c.Quarantined && !c.WorkersHeld
}
