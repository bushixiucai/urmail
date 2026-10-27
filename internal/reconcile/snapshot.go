package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/certs"
	"github.com/bushixiucai/urmail/internal/config"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mail"
	"github.com/bushixiucai/urmail/internal/mail/dovecot"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/mail/opendkim"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
	"github.com/bushixiucai/urmail/internal/store"
)

// StoreQuerier abstracts the platform queries needed to assemble snapshots.
type StoreQuerier interface {
	PlatformListSpamDeliveryPolicies(context.Context) ([]store.PlatformListSpamDeliveryPoliciesRow, error)
	PlatformListManagedDNS(context.Context) ([]store.PlatformListManagedDNSRow, error)
	PlatformListDomains(ctx context.Context) ([]store.PlatformListDomainsRow, error)
	PlatformListMailboxes(ctx context.Context) ([]store.PlatformListMailboxesRow, error)
	PlatformListAliases(ctx context.Context) ([]store.PlatformListAliasesRow, error)
	PlatformListDKIMDomains(ctx context.Context) ([]store.PlatformListDKIMDomainsRow, error)
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

	dkimRows, err := q.PlatformListDKIMDomains(ctx)
	if err != nil {
		return nil, fmt.Errorf("list DKIM domains for snapshot: %w", err)
	}
	dkimDomains := make([]opendkim.DomainKey, 0, len(dkimRows))
	dkimByDomain := make(map[string]opendkim.DomainKey, len(dkimRows))
	for _, row := range dkimRows {
		if !row.DkimPrivateKey.Valid || row.DkimPrivateKey.String == "" {
			continue
		}
		dkimDomains = append(dkimDomains, opendkim.DomainKey{
			Domain:     row.Fqdn,
			Selector:   row.DkimSelector,
			PrivateKey: row.DkimPrivateKey.String,
		})
		dkimByDomain[strings.ToLower(row.Fqdn)] = opendkim.DomainKey{
			Domain:     row.Fqdn,
			Selector:   row.DkimSelector,
			PrivateKey: row.DkimPrivateKey.String,
		}
	}

	spamRows, err := q.PlatformListSpamDeliveryPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list private spam delivery policies: %w", err)
	}
	if len(spamRows) > 10000 {
		return nil, fmt.Errorf("private spam delivery policy limit exceeded")
	}
	spamPolicies := make([]dovecot.SpamPolicy, 0, len(spamRows))
	for _, r := range spamRows {
		spamPolicies = append(spamPolicies, dovecot.SpamPolicy{MailboxAddress: r.MailboxAddress, Scope: r.Scope, SenderAddress: r.SenderAddress, SenderDomain: r.SenderDomain, List: r.List})
	}

	hostname := cfg.MyHostname
	if hostname == "" {
		hostname = "box." + cfg.PrimaryDomain
	}
	storageRoot := cfg.StorageRoot
	if storageRoot == "" {
		storageRoot = "/var/mail"
	}
	certFile := filepath.Join("/var/lib/urmail-agent/certs/mail", hostname+".cert")
	keyFile := filepath.Join("/var/lib/urmail-agent/certs/mail", hostname+".key")

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
			ID: m.ID, TenantID: m.TenantID,
			Address: m.Address,
			Status:  m.Status,
		})
	}

	pfAliases := make([]postfix.Alias, 0, len(aliases))
	for _, a := range aliases {
		pfAliases = append(pfAliases, postfix.Alias{
			Domain:       a.Domain,
			LocalPart:    a.LocalPart,
			Target:       a.Target,
			Kind:         a.Kind,
			SenderLogins: a.SenderLogins,
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
	managedRows, err := q.PlatformListManagedDNS(ctx)
	if err != nil {
		return nil, fmt.Errorf("list confirmed managed DNS: %w", err)
	}
	managedRecords := map[string][]nsd.Record{}
	for _, row := range managedRows {
		var records []nsd.Record
		if json.Unmarshal(row.Records, &records) != nil {
			return nil, fmt.Errorf("invalid confirmed managed DNS records")
		}
		managedRecords[row.Fqdn] = records
	}
	nsdZones := make([]nsd.Zone, 0, len(domains))
	for _, d := range domains {
		if d.Mode == "delegated" || (d.ProvisioningState == "pending" && !d.DnsExplicit) {
			continue
		}
		updated := time.Now().UTC()
		if d.UpdatedAt.Valid {
			updated = d.UpdatedAt.Time.UTC()
		}
		var records []nsd.Record
		if d.DnsExplicit {
			var found bool
			records, found = managedRecords[d.Fqdn]
			if !found {
				return nil, fmt.Errorf("confirmed DNS zone missing")
			}
		}
		if dk, ok := dkimByDomain[strings.ToLower(d.Fqdn)]; ok && !d.DnsExplicit {
			txt, err := domainMail.PublicKeyTXT(dk.PrivateKey)
			if err != nil {
				return nil, fmt.Errorf("derive DKIM public key for %s: %w", d.Fqdn, err)
			}
			records = append(records, nsd.Record{
				Name:  dk.Selector + "._domainkey",
				Type:  "TXT",
				Value: txt,
			})
		}
		nsdZones = append(nsdZones, nsd.Zone{
			Domain:   d.Fqdn,
			Mode:     "managed",
			Serial:   ZoneSerial(updated),
			Records:  records,
			Explicit: d.DnsExplicit,
		})
	}

	// 3. Nginx Snapshot
	nginxDomains := make([]nginx.DomainConfig, 0, len(domains))
	for _, d := range domains {
		domainCert := filepath.Join(certs.DomainCertDir, d.Fqdn+".cert")
		domainKey := filepath.Join(certs.DomainCertDir, d.Fqdn+".key")
		// Do not emit a server block whose certificate does not exist yet:
		// nginx -t would fail and block every other tenant's configuration.
		// The certificate worker (F3) adds the block after issuance.
		if _, err := os.Stat(domainCert); err != nil {
			continue
		}
		if _, err := os.Stat(domainKey); err != nil {
			continue
		}
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

	result := &mail.ConfigContext{
		PostfixNode: postfix.NodeConfig{
			Hostname:                 hostname,
			PrimaryDomain:            cfg.PrimaryDomain,
			StorageRoot:              storageRoot,
			TLSCertFile:              certFile,
			TLSKeyFile:               keyFile,
			PolicyEnabled:            cfg.PolicyEnabled,
			V80Preserve:              cfg.PostfixV80Preserve,
			PreserveExternalOpenDKIM: cfg.PreserveExternalOpenDKIM,
			PolicySocket:             cfg.PolicySocket,
			ArchiveEnabled:           cfg.ArchiveEnabled,
		},
		PostfixSnap: pfSnap,

		DovecotNode: dovecot.NodeConfig{
			SpamPolicies:  spamPolicies,
			V80Preserve:   cfg.PostfixV80Preserve,
			Hostname:      hostname,
			PrimaryDomain: cfg.PrimaryDomain,
			StorageRoot:   storageRoot,
			TLSCertFile:   certFile,
			TLSKeyFile:    keyFile,
			DatabaseURL:   dovecotDB,
		},

		NSDNode: nsd.NodeConfig{
			ListenAddresses: cfg.DNSListenAddresses,
			Hostname:        hostname,
			PrimaryDomain:   cfg.PrimaryDomain,
			PublicIPv4:      publicIPv4,
			Nameservers:     nameservers,
		},
		NSDSnap: nsd.Snapshot{Zones: nsdZones},

		NginxNode: nginx.NodeConfig{
			PrimaryDomain: cfg.PrimaryDomain,
			StorageRoot:   storageRoot,
			// nginx backends are validated to carry an explicit scheme.
			BackendHTTP:    withHTTPScheme(cfg.ListenAddr),
			WebmailBackend: webmailBackend(cfg),
		},
		NginxSnap: nginx.Snapshot{Domains: nginxDomains},

		DKIM: opendkim.Snapshot{Domains: dkimDomains},
	}
	if err := reserveZoneSerials(ctx, q, result.NSDNode, &result.NSDSnap); err != nil {
		return nil, err
	}
	return result, nil
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

func webmailBackend(cfg *config.Config) string {
	if cfg.RoundcubeEnabled {
		return "http://127.0.0.1:8888"
	}
	return ""
}
