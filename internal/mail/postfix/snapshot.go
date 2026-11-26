package postfix

import "path/filepath"

// NodeConfig holds the node-level configuration required for Postfix generation.
type NodeConfig struct {
	Hostname                 string // myhostname (URMAIL_MYHOSTNAME, RFC 1123)
	PrimaryDomain            string // mydomain/myorigin (RFC 1123)
	StorageRoot              string // URMAIL_STORAGE_ROOT
	TLSCertFile              string // path to TLS certificate
	TLSKeyFile               string // path to TLS private key
	V80Preserve              bool   // Fixed reviewed v80 SMTP profile; requires PolicyEnabled.
	PreserveExternalOpenDKIM bool   // Keep live OpenDKIM on 8891; do not point Postfix at 12301.
	PolicyEnabled            bool   // URMAIL_POLICY_ENABLED, default false
	PolicySocket             string // default private/policy (relative to Postfix chroot)
	ArchiveEnabled           bool   // URMAIL_ARCHIVE_ENABLED, default false
	ArchiveSocket            string // default private/urmail-archive (relative to Postfix chroot)
	MessageSizeLimit         int64  // custom message_size_limit; defaults to 25MiB (26214400) when specified
	// ConfigDir is the absolute Postfix config_dir the rendered main.cf will
	// be read from (URMAIL_RENDERED_CONFIG_DIR + "/postfix" on compose
	// deployments, empty for the historical /etc/postfix host layout). The
	// file-based maps must reference their absolute mount-surface paths, so
	// the renderer cannot hardcode /etc/postfix.
	ConfigDir string
}

// defaultConfigDir is the host-layout Postfix config directory used when
// ConfigDir is empty.
const defaultConfigDir = "/etc/postfix"

// EffectiveConfigDir returns the config_dir main.cf is rendered for. Postfix
// resolves file maps from main.cf by absolute path only (relative map specs
// resolve against the process working directory, not -c), so every map
// reference must carry this prefix.
func (n NodeConfig) EffectiveConfigDir() string {
	if n.ConfigDir == "" {
		return defaultConfigDir
	}
	return filepath.Clean(n.ConfigDir)
}

// MapRoot is the absolute directory the urmail map files live in for this
// deployment shape. It always equals <config_dir>/urmail, which is where the
// agent writes FileVirtualMailboxMap and friends relative to the render root.
func (n NodeConfig) MapRoot() string {
	return filepath.Join(n.EffectiveConfigDir(), "urmail")
}

// Domain represents a mail domain in the database snapshot.
type Domain struct {
	FQDN   string
	Status string // must be "active"
}

// Mailbox represents an active mailbox in the database snapshot.
type Mailbox struct {
	ID       int64
	TenantID int64
	Address  string // local_part@fqdn
	Status   string // must be "active"
}

// Alias represents an alias or catchall entry in the database snapshot.
type Alias struct {
	Domain       string
	LocalPart    string   // "" (empty string) for catchall
	Target       string   // local or external address
	Kind         string   // alias|catchall|forward
	SenderLogins []string // Explicit authenticated mailboxes; never inferred from Target.
}

// Snapshot contains the platform-level mail domains, mailboxes, and aliases.
type Snapshot struct {
	Domains   []Domain
	Mailboxes []Mailbox
	Aliases   []Alias
}
