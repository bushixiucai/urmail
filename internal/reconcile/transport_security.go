package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/certs"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/maildnsreport"
	"github.com/bushixiucai/urmail/internal/mailtransport"
	"github.com/bushixiucai/urmail/internal/store"
)

func attachTransportSecurity(ctx context.Context, q StoreQuerier, zones []nsd.Zone, web *[]nginx.DomainConfig, host string) error {
	rows, err := q.PlatformListTransportSecurity(ctx)
	if err != nil {
		return err
	}
	if len(rows) > 10000 {
		return fmt.Errorf("transport policy count exceeds limit")
	}
	for _, row := range rows {
		publication := mailtransport.Publication(store.DomainTransportSecurity{MtastsPolicyID: row.MtastsPolicyID, MtastsMode: row.MtastsMode, PolicyMx: row.PolicyMx, MaxAge: row.MaxAge, TlsRptRua: row.TlsRptRua, Enabled: row.Enabled, CacheSafeAfter: row.CacheSafeAfter})
		if err := publication.CheckMXChange(host, time.Now().UTC()); err != nil {
			return err
		}
		for i := range zones {
			if zones[i].Domain != row.Fqdn {
				continue
			}

			for _, record := range publication.DNSRecords(host) {
				matched := -1
				for index, existing := range zones[i].Records {
					name := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(existing.Name), "."), "."+row.Fqdn)
					if !strings.EqualFold(name, record.Name) {
						continue
					}
					if matched >= 0 {
						return fmt.Errorf("TRANSPORT_DNS_CONFLICT: %s", row.Fqdn)
					}
					matched = index
				}
				desired := nsd.Record{Name: record.Name, Type: record.Type, Value: record.Value, TTL: 300}
				if matched < 0 {
					zones[i].Records = append(zones[i].Records, desired)
					continue
				}
				previous := zones[i].Records[matched]
				if previous.Type == record.Type && strings.TrimSuffix(previous.Value, ".") == strings.TrimSuffix(record.Value, ".") {
					continue
				}
				if !maildnsreport.ManagedTransportRecord(record.Name, previous.Type, previous.Value) || !maildnsreport.ManagedTransportRecord(record.Name, record.Type, record.Value) {
					return fmt.Errorf("TRANSPORT_DNS_CONFLICT: %s", row.Fqdn)
				}
				zones[i].Records[matched] = desired
			}

		}
		fqdn := "mta-sts." + row.Fqdn
		cert := filepath.Join(certs.DomainCertDir, fqdn+".cert")
		key := filepath.Join(certs.DomainCertDir, fqdn+".key")
		if _, err = os.Stat(cert); err != nil {
			continue
		}
		if _, err = os.Stat(key); err != nil {
			continue
		}
		*web = append(*web, nginx.DomainConfig{FQDN: fqdn, TLSCertFile: cert, TLSKeyFile: key, TransportOnly: true})
	}
	return nil
}
