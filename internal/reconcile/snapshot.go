package reconcile

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/mail"
	"github.com/bushixiucai/urmail/internal/mail/dovecot"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
	"github.com/bushixiucai/urmail/internal/store"
)

// StoreQuerier abstracts the platform queries needed to assemble snapshots.
type StoreQuerier interface {
	PlatformListDomains(ctx context.Context) ([]store.PlatformListDomainsRow, error)
	PlatformListMailboxes(ctx context.Context) ([]store.PlatformListMailboxesRow, error)
	PlatformListAliases(ctx context.Context) ([]store.PlatformListAliasesRow, error)
}

// AssembleConfigContext builds the complete ConfigContext for all mail services
// by querying the database store and combining it with node-level runtime config.
func AssembleConfigContext(ctx context.Context, cfg *config.Config, q StoreQuerier, publicIPv4 string, nameservers []string) (*mail.ConfigContext, error) {
	domains, err := q.PlatformListDomains(ctx)
	if err != nil {
		return nil, fmt.Errorf("list domains for snapshot: %w", err)
	}

	mailboxes, err := q.PlatformListMailboxes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list mailboxes for snapshot: %w", err)
	}

	aliases, err := q.PlatformListAliases(ctx)
	if err != nil {
		return nil, fmt.Errorf("list aliases for snapshot: %w", err)
	}

	hostname := cfg.MyHostname
	if hostname == "" {
		hostname = "box." + cfg.PrimaryDomain
	}
	storageRoot := cfg.StorageRoot
	if storageRoot == "" {
		storageRoot = "/var/mail"
	}
	certFile := filepath.Join(storageRoot, "ssl", hostname+".cert")
	keyFile := filepath.Join(storageRoot, "ssl", hostname+".key")

	// 1. Postfix Snapshot
	pfDomains := make([]postfix.Domain, 0, len(domains))
	for _, d := range domains {
		pfDomains = append(pfDomains, postfix.Domain{
			FQDN:   d.Fqdn,
			Status: d.Status,
		})
	}

	pfMailboxes := make([]postfix.Mailbox, 0, len(mailboxes))
	for _, m := range mailboxes {
		pfMailboxes = append(pfMailboxes, postfix.Mailbox{
			Address: m.Address,
			Status:  m.Status,
		})
	}

	pfAliases := make([]postfix.Alias, 0, len(aliases))
	for _, a := range aliases {
		pfAliases = append(pfAliases, postfix.Alias{
			Domain:    a.Domain,
			LocalPart: a.LocalPart,
			Target:    a.Target,
			Kind:      a.Kind,
		})
	}

	pfSnap := postfix.Snapshot{
		Domains:   pfDomains,
		Mailboxes: pfMailboxes,
		Aliases:   pfAliases,
	}

	// 2. NSD Snapshot
	nsdZones := make([]nsd.Zone, 0, len(domains))
	for _, d := range domains {
		nsdZones = append(nsdZones, nsd.Zone{
			Domain: d.Fqdn,
			Mode:   "managed",
			Serial: 2026102201,
		})
	}

	// 3. Nginx Snapshot
	nginxDomains := make([]nginx.DomainConfig, 0, len(domains))
	for _, d := range domains {
		domainCert := filepath.Join(storageRoot, "ssl", d.Fqdn+".cert")
		domainKey := filepath.Join(storageRoot, "ssl", d.Fqdn+".key")
		nginxDomains = append(nginxDomains, nginx.DomainConfig{
			FQDN:              d.Fqdn,
			TLSCertFile:       domainCert,
			TLSKeyFile:        domainKey,
			ClientMaxBodySize: "128M",
			EnableWebmail:     true,
		})
	}

	if len(nameservers) == 0 {
		nameservers = []string{"ns1." + cfg.PrimaryDomain}
	}
	if publicIPv4 == "" {
		publicIPv4 = "127.0.0.1"
	}

	return &mail.ConfigContext{
		PostfixNode: postfix.NodeConfig{
			Hostname:       hostname,
			PrimaryDomain:  cfg.PrimaryDomain,
			StorageRoot:    storageRoot,
			TLSCertFile:    certFile,
			TLSKeyFile:     keyFile,
			PolicyEnabled:  cfg.PolicyEnabled,
			PolicySocket:   strings.TrimPrefix(cfg.PolicySocket, "/run/urmail/"),
			ArchiveEnabled: cfg.ArchiveEnabled,
		},
		PostfixSnap: pfSnap,

		DovecotNode: dovecot.NodeConfig{
			Hostname:      hostname,
			PrimaryDomain: cfg.PrimaryDomain,
			StorageRoot:   storageRoot,
			TLSCertFile:   certFile,
			TLSKeyFile:    keyFile,
			DatabaseURL:   cfg.DatabaseURL,
		},

		NSDNode: nsd.NodeConfig{
			Hostname:      hostname,
			PrimaryDomain: cfg.PrimaryDomain,
			PublicIPv4:    publicIPv4,
			Nameservers:   nameservers,
		},
		NSDSnap: nsd.Snapshot{Zones: nsdZones},

		NginxNode: nginx.NodeConfig{
			PrimaryDomain: cfg.PrimaryDomain,
			StorageRoot:   storageRoot,
			BackendHTTP:   cfg.ListenAddr,
		},
		NginxSnap: nginx.Snapshot{Domains: nginxDomains},
	}, nil
}
