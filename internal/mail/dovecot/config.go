// Package dovecot generates canonical Dovecot configuration.
package dovecot

// NodeConfig holds the node-level configuration required for Dovecot generation.
type NodeConfig struct {
	Hostname      string // myhostname (RFC 1123)
	PrimaryDomain string // platform primary domain (RFC 1123)
	StorageRoot   string // URMAIL_STORAGE_ROOT (e.g. /var/mail)
	TLSCertFile   string // path to TLS certificate
	TLSKeyFile    string // path to TLS private key
	DatabaseURL   string // PostgreSQL connection string for urmail_dovecot
	MailUID       string // UID for vmail user (default "mail")
	MailGID       string // GID for vmail group (default "mail")
}

// Snapshot represents the database snapshot needed for Dovecot configuration.
// Note: Since userdb & passdb dynamically query PostgreSQL via urmail_dovecot,
// Dovecot configuration files themselves do not need to inline every mailbox/domain.
type Snapshot struct{}
