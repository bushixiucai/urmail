package postfix

// NodeConfig holds the node-level configuration required for Postfix generation.
type NodeConfig struct {
	Hostname       string // myhostname (URMAIL_MYHOSTNAME, RFC 1123)
	PrimaryDomain  string // mydomain/myorigin (RFC 1123)
	StorageRoot    string // URMAIL_STORAGE_ROOT
	TLSCertFile    string // path to TLS certificate
	TLSKeyFile     string // path to TLS private key
	PolicyEnabled  bool   // URMAIL_POLICY_ENABLED, default false
	PolicySocket   string // default private/policy (relative to Postfix chroot)
	ArchiveEnabled bool   // URMAIL_ARCHIVE_ENABLED, default false
	ArchiveSocket  string // default private/urmail-archive (relative to Postfix chroot)
}

// Domain represents a mail domain in the database snapshot.
type Domain struct {
	FQDN   string
	Status string // must be "active"
}

// Mailbox represents an active mailbox in the database snapshot.
type Mailbox struct {
	Address string // local_part@fqdn
	Status  string // must be "active"
}

// Alias represents an alias or catchall entry in the database snapshot.
type Alias struct {
	Domain    string
	LocalPart string // "" (empty string) for catchall
	Target    string // local or external address
	Kind      string // alias|catchall|forward
}

// Snapshot contains the platform-level mail domains, mailboxes, and aliases.
type Snapshot struct {
	Domains   []Domain
	Mailboxes []Mailbox
	Aliases   []Alias
}
