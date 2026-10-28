// Package nginx generates canonical tenant reverse proxy configuration.
package nginx

// NodeConfig holds global node web server options.
type NodeConfig struct {
	PrimaryDomain  string // e.g. urmail.cn
	StorageRoot    string // e.g. /var/mail
	BackendHTTP    string // e.g. 127.0.0.1:8080 (urmaild)
	WebmailBackend string // e.g. 127.0.0.1:8888 (optional roundcube)
	ZPushBackend   string // e.g. 127.0.0.1:8889 (optional z-push)
}

// DomainConfig specifies TLS and proxy settings for an active web domain.
type DomainConfig struct {
	ACMEOnly          bool   // Verified brand waiting for a trusted certificate; expose no HTTPS product routes.
	TransportOnly     bool   // Expose only the MTA-STS policy, never panel or mailbox routes.
	FQDN              string // e.g. mail.example.com
	TLSCertFile       string // path to TLS cert
	TLSKeyFile        string // path to TLS key
	ClientMaxBodySize string // e.g. "128M" or "256M", default "128M"
	EnableWebmail     bool   // whether /mail proxy is enabled for this domain
}

// Snapshot represents the set of domains to be configured.
type Snapshot struct {
	Domains []DomainConfig
}

// ACMEWebroot is root-owned and publicly traversable by nginx workers.
const ACMEWebroot = "/var/lib/urmail-acme"
