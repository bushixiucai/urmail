package reconcile

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

	// 2. NSD Snapshot. Delegated domains are DNS-hosted elsewhere and must
	// not be rendered as managed zones. The SOA serial is derived from the
	// domain row's updated_at (YYYYMMDDNN), never hardcoded.
	nsdZones := make([]nsd.Zone, 0, len(domains))
	for _, d := range domains {
		if d.Mode == "delegated" {
			continue
		}
		updated := time.Now().UTC()
		if d.UpdatedAt.Valid {
			updated = d.UpdatedAt.Time.UTC()
		}
		nsdZones = append(nsdZones, nsd.Zone{
			Domain: d.Fqdn,
			Mode:   "managed",
			Serial: ZoneSerial(updated),
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
	dovecotDB := cfg.DatabaseURLDovecot
	if dovecotDB == "" {
		// Development fallbacks; production sets URMAIL_DATABASE_URL_DOVECOT.
		dovecotDB = cfg.DatabaseURLPlatform
	}
	if dovecotDB == "" {
		dovecotDB = cfg.DatabaseURL
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
			DatabaseURL:   dovecotDB,
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
			// nginx backends are validated to carry an explicit scheme.
			BackendHTTP: withHTTPScheme(cfg.ListenAddr),
		},
		NginxSnap: nginx.Snapshot{Domains: nginxDomains},
	}, nil
}

// ZoneSerial derives an NSD SOA serial from a timestamp in the YYYYMMDDNN
// form: the UTC date plus a two-digit intra-day sequence (96 fifteen-minute
// buckets). Buckets keep serials increasing within a day without a shared
// counter, and a domain edit moves updated_at and therefore the serial.
func ZoneSerial(t time.Time) uint32 {
	date := t.Format("20060102")
	bucket := (t.Hour()*60 + t.Minute()) / 15 // 0..95
	if bucket < 0 {
		bucket = 0
	}
	if bucket > 98 {
		bucket = 98
	}
	n, err := strconv.ParseUint(fmt.Sprintf("%s%02d", date, bucket+1), 10, 32)
	if err != nil {
		return 1
	}
	return uint32(n)
}
