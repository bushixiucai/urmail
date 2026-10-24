package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/dnsprovider"
	domainmail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/store"
)

// SecretBox reuses the existing platform encryption format.
type SecretBox interface {
	EncryptString(string) (string, error)
	DecryptString(string) (string, error)
}

// Factory can be replaced only by a trusted local test harness, not API input.
type Factory func(string, dnsprovider.Credentials) (dnsprovider.RecordManager, error)

// Service holds no durable work in memory; PostgreSQL owns plans and attempts.
type Service struct {
	Pool        *db.Pool
	Secrets     SecretBox
	Hostname    string
	Factory     Factory
	AfterChange func()
}

func (s *Service) manager(provider string, credentials dnsprovider.Credentials) (dnsprovider.RecordManager, error) {
	if s.Factory != nil {
		return s.Factory(provider, credentials)
	}
	return dnsprovider.NewRecordManager(provider, credentials)
}
func (s *Service) credentialManager(ctx context.Context, tenant, domain int64) (store.DnsCredentials, dnsprovider.RecordManager, error) {
	row, err := store.New(s.Pool.Pool).PlatformGetDNSCredential(ctx, store.PlatformGetDNSCredentialParams{TenantID: tenant, DomainID: domain})
	if err != nil {
		return row, nil, errors.New("DNS_CREDENTIAL_UNAVAILABLE")
	}
	if s.Secrets == nil {
		return row, nil, errors.New("DNS_CREDENTIAL_UNAVAILABLE")
	}
	plain, err := s.Secrets.DecryptString(row.EncryptedSecret)
	if err != nil {
		return row, nil, errors.New("DNS_CREDENTIAL_UNAVAILABLE")
	}
	var creds dnsprovider.Credentials
	if json.Unmarshal([]byte(plain), &creds) != nil {
		return row, nil, errors.New("DNS_CREDENTIAL_UNAVAILABLE")
	}
	manager, err := s.manager(row.Provider, creds)
	return row, manager, err
}

// DomainDigest binds a plan to the exact verified identity, mode and public
// signing key. The private DKIM key is never included in a plan or its digest.
func DomainDigest(d store.PlatformDNSDomainRow, hostname string) (string, []dnsprovider.Record, error) {
	if d.Status != "active" || !d.VerifiedAt.Valid {
		return "", nil, errors.New("DNS_OWNERSHIP_REQUIRED")
	}
	if !d.DkimPrivateKey.Valid {
		return "", nil, errors.New("DNS_DKIM_UNAVAILABLE")
	}
	key, err := domainmail.PublicKeyTXT(d.DkimPrivateKey.String)
	if err != nil {
		return "", nil, errors.New("DNS_DKIM_UNAVAILABLE")
	}
	desired := dnsprovider.GenerateMailRecords(d.Fqdn, hostname, d.DkimSelector, key, "")
	for i := range desired {
		if desired[i].Name == "@" {
			desired[i].Name = d.Fqdn
		}
	}
	return Digest(struct {
		ID, Tenant                            int64
		FQDN, Mode, Selector, PublicKey, Host string
	}{d.ID, d.TenantID, d.Fqdn, d.Mode, d.DkimSelector, key, hostname}), desired, nil
}

// Draft contains only public records and version references.
type Draft struct {
	Kind, DomainDigest, SourceDigest string
	CredentialID, CredentialVersion  pgtype.Int8
	Before                           []dnsprovider.RemoteRecord
	Desired                          []dnsprovider.Record
	Changes                          Changes
}

// Prepare reads upstream data but cannot write DNS or mark a plan confirmed.
func (s *Service) Prepare(ctx context.Context, tenant, domain int64, kind string, imported []dnsprovider.RemoteRecord) (Draft, error) {
	out := Draft{Kind: kind}
	d, err := store.New(s.Pool.Pool).PlatformDNSDomain(ctx, store.PlatformDNSDomainParams{TenantID: tenant, ID: domain})
	if err != nil {
		return out, errors.New("DNS_OWNERSHIP_REQUIRED")
	}
	out.DomainDigest, out.Desired, err = DomainDigest(d, s.Hostname)
	if err != nil {
		return out, err
	}
	switch kind {
	case "provider":
		cred, manager, e := s.credentialManager(ctx, tenant, domain)
		if e != nil {
			return out, e
		}
		zone, e := manager.Probe(ctx, cred.ZoneName)
		if e != nil || zone.ID != cred.ZoneID || zone.Name != cred.ZoneName {
			return out, errors.New("DNS_ZONE_CHANGED")
		}
		out.Before, err = manager.List(ctx, zone)
		if err != nil {
			return out, err
		}
		out.CredentialID = pgtype.Int8{Int64: cred.ID, Valid: true}
		out.CredentialVersion = pgtype.Int8{Int64: cred.Version, Valid: true}
	case "managed":
		if d.Mode != "managed" {
			return out, errors.New("DNS_MODE_MISMATCH")
		}
		out.Before = imported
	case "manual":
		if d.Mode != "delegated" {
			return out, errors.New("DNS_MODE_MISMATCH")
		}
		out.Before = imported
	default:
		return out, errors.New("DNS_PLAN_INVALID")
	}
	if kind != "provider" {
		for i := range out.Before {
			if out.Before[i].Name == "@" {
				out.Before[i].Name = d.Fqdn
			}
			out.Before[i].ID = "import:" + strconv.Itoa(i)
		}
	}
	out.Changes, err = BuildChanges(d.Fqdn, out.Before, out.Desired)
	if err != nil {
		return out, err
	}
	out.SourceDigest = SnapshotDigest(out.Before)
	if kind == "managed" {
		if _, err = ManagedRecords(d.Fqdn, out.Before, out.Changes); err != nil {
			return out, err
		}
	}
	return out, nil
}

// ManagedRecords imports explicit tenant records and applies the confirmed
// mail additions without replacing website addresses or implicit apex data.
func ManagedRecords(domain string, before []dnsprovider.RemoteRecord, changes Changes) ([]nsd.Record, error) {
	if len(changes.Conflicts) > 0 {
		return nil, errors.New("DNS_PLAN_HAS_CONFLICTS")
	}
	records := make([]dnsprovider.RemoteRecord, 0, len(before)+len(changes.Actions))
	records = append(records, before...)
	for _, action := range changes.Actions {
		if action.Before != nil {
			found := false
			for i := range records {
				if records[i].ID == action.Before.ID && EqualValue(records[i].Record, action.Before.Record) {
					records[i].Record = action.After
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("DNS_SOURCE_CHANGED")
			}
		} else {
			records = append(records, dnsprovider.RemoteRecord{Record: action.After})
		}
	}
	out := make([]nsd.Record, 0, len(records))
	for _, r := range records {
		record := canonical(r.Record)
		if record.Name == "@" {
			record.Name = domain
		}
		if record.Name != domain && !strings.HasSuffix(record.Name, "."+domain) {
			return nil, errors.New("DNS_RECORD_OUTSIDE_OWNED_DOMAIN")
		}
		if record.Type == "SOA" || (record.Type == "NS" && record.Name == domain) {
			continue
		}
		name := strings.TrimSuffix(record.Name, "."+domain)
		if record.Name == domain {
			name = "@"
		}
		out = append(out, nsd.Record{Name: name, Type: string(record.Type), Value: record.Value, Prio: record.Priority, TTL: record.TTL})
	}
	_, err := nsd.Generate(nsd.NodeConfig{ListenAddresses: []string{"127.0.0.1"}, Hostname: "mail.example.test", PrimaryDomain: "example.test", PublicIPv4: "192.0.2.1", Nameservers: []string{"ns.example.test"}}, nsd.Snapshot{Zones: []nsd.Zone{{Domain: domain, Mode: "managed", Serial: 1, Explicit: true, Records: out}}})
	if err != nil {
		return nil, errors.New("DNS_MANAGED_RECORD_INVALID")
	}
	return out, nil
}

// NewManager exposes the fixed-endpoint provider factory to the credential
// probe handler without persisting or returning a plaintext credential.
func (s *Service) NewManager(provider string, credentials dnsprovider.Credentials) (dnsprovider.RecordManager, error) {
	return s.manager(provider, credentials)
}
