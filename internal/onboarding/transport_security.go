package onboarding

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/dnsprovider"
	"github.com/bushixiucai/urmail/internal/mailtransport"
	"github.com/bushixiucai/urmail/internal/store"
)

func (s *Service) domainDigest(ctx context.Context, domain store.PlatformDNSDomainRow) (string, []dnsprovider.Record, error) {
	digest, records, err := DomainDigest(domain, s.Hostname)
	if err != nil {
		return "", nil, err
	}
	// Platform worker pass: this read is tenant-domain by annotation; bind it
	// to the tenant of the platform-fetched domain row so the scope stays exact.
	tenantCtx := isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: domain.TenantID})
	policy, err := db.Queries(s.Pool).GetDomainTransportSecurity(tenantCtx, store.GetDomainTransportSecurityParams{TenantID: domain.TenantID, DomainID: domain.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return digest, records, nil
	}
	if err != nil {
		return "", nil, err
	}
	publication := mailtransport.Publication(policy)
	if err = publication.CheckMXChange(s.Hostname, time.Now().UTC()); err != nil {
		return "", nil, err
	}
	for _, record := range publication.DNSRecords(s.Hostname) {
		records = append(records, dnsprovider.Record{Name: record.Name + "." + domain.Fqdn, Type: dnsprovider.RecordType(record.Type), Value: strings.TrimSuffix(record.Value, "."), TTL: 300})
	}
	return Digest(struct {
		Domain      string
		Publication string
		Records     []dnsprovider.Record
	}{digest, publication.Digest(), records}), records, nil
}
