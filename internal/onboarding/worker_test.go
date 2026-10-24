package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/dnsprovider"
	domainmail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

type dnsStandIn struct {
	mu          sync.Mutex
	records     []dnsprovider.RemoteRecord
	writes      int
	unknown     bool
	beforeWrite func()
}

func (f *dnsStandIn) Probe(context.Context, string) (dnsprovider.Zone, error) {
	return dnsprovider.Zone{ID: "zone", Name: "example.test"}, nil
}
func (f *dnsStandIn) List(context.Context, dnsprovider.Zone) ([]dnsprovider.RemoteRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dnsprovider.RemoteRecord(nil), f.records...), nil
}
func (f *dnsStandIn) Create(_ context.Context, _ dnsprovider.Zone, r dnsprovider.Record) (dnsprovider.RemoteRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeWrite != nil {
		f.beforeWrite()
	}
	f.writes++
	row := dnsprovider.RemoteRecord{ID: fmt.Sprintf("created-%d", f.writes), Record: r}
	f.records = append(f.records, row)
	if f.unknown {
		f.unknown = false
		return dnsprovider.RemoteRecord{}, dnsprovider.ErrProvider
	}
	return row, nil
}
func (f *dnsStandIn) Update(_ context.Context, _ dnsprovider.Zone, old dnsprovider.RemoteRecord, r dnsprovider.Record) (dnsprovider.RemoteRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	for i := range f.records {
		if f.records[i].ID == old.ID {
			f.records[i].Record = r
			return f.records[i], nil
		}
	}
	return dnsprovider.RemoteRecord{}, dnsprovider.ErrProvider
}
func (f *dnsStandIn) Delete(_ context.Context, _ dnsprovider.Zone, old dnsprovider.RemoteRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	for i := range f.records {
		if f.records[i].ID == old.ID {
			f.records = append(f.records[:i], f.records[i+1:]...)
			return nil
		}
	}
	return dnsprovider.ErrProvider
}

type onboardingFixture struct {
	s                    *Service
	p, a                 *db.Pool
	tenant, domain, user int64
	upstream             *dnsStandIn
}

func newFixture(t *testing.T) *onboardingFixture {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.New(t)
	migration, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = migration.Up(ctx); err != nil {
		t.Fatal(err)
	}
	migration.Close()
	p, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	a, err := db.OpenApp(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	f := &onboardingFixture{p: p, a: a}
	var brand int64
	if err = p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('onboarding','Onboarding') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Onboarding','onboarding','active') RETURNING id", brand).Scan(&f.tenant); err != nil {
		t.Fatal(err)
	}
	key, err := domainmail.GenerateDKIMKeypair("mail")
	if err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,mode,status,verification_token,verified_at,dkim_selector,dkim_private_key,provisioning_state,custom_web_enabled) VALUES($1,'example.test','delegated','active','proof',now(),'mail',$2,'pending',false) RETURNING id", f.tenant, key.PrivateKey).Scan(&f.domain); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('onboarding@example.test','unused') RETURNING id").Scan(&f.user); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", f.user, f.tenant); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	f.upstream = &dnsStandIn{records: []dnsprovider.RemoteRecord{{ID: "website", Record: dnsprovider.Record{Type: "A", Name: "example.test", Value: "192.0.2.55", TTL: 300}}, {ID: "spf", Record: dnsprovider.Record{Type: "TXT", Name: "example.test", Value: "v=spf1 include:thirdparty.example ~all", TTL: 300}}}}
	f.s = &Service{Pool: p, Secrets: box, Hostname: "mail.platform.test", Factory: func(string, dnsprovider.Credentials) (dnsprovider.RecordManager, error) { return f.upstream, nil }}
	cipher, err := box.EncryptString(`{"token":"fixture-secret"}`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := a.UserTx(ctx, f.tenant, f.user)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = store.New(tx).SaveDNSCredential(ctx, store.SaveDNSCredentialParams{TenantID: f.tenant, DomainID: f.domain, Provider: "cloudflare", ZoneName: "example.test", ZoneID: "zone", EncryptedSecret: cipher}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *onboardingFixture) plan(t *testing.T) store.DnsChangePlans {
	t.Helper()
	ctx := context.Background()
	draft, err := f.s.Prepare(ctx, f.tenant, f.domain, "provider", nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(draft.Before)
	desired, _ := json.Marshal(draft.Desired)
	changes, _ := json.Marshal(draft.Changes)
	var id pgtype.UUID
	if err = id.Scan("11111111-1111-4111-8111-111111111111"); err != nil {
		t.Fatal(err)
	}
	tx, err := f.a.UserTx(ctx, f.tenant, f.user)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if _, err = q.CreateDNSPlan(ctx, store.CreateDNSPlanParams{ID: id, TenantID: f.tenant, DomainID: f.domain, CredentialID: draft.CredentialID, CredentialVersion: draft.CredentialVersion, Kind: "provider", DomainDigest: draft.DomainDigest, SourceDigest: draft.SourceDigest, BeforeRecords: before, DesiredRecords: desired, Changes: changes}); err != nil {
		t.Fatal(err)
	}
	if err = q.ConfirmDNSPlan(ctx, store.ConfirmDNSPlanParams{TenantID: f.tenant, ID: id, SourceDigest: draft.SourceDigest}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := store.New(f.p.Pool).GetDNSPlan(ctx, store.GetDNSPlanParams{TenantID: f.tenant, ID: id})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestConfirmedDNSPlanAppliesAndRollsBackOnlyOwnChanges(t *testing.T) {
	f := newFixture(t)
	p := f.plan(t)
	ctx := context.Background()
	if worked, err := f.s.DNSOnce(ctx, "worker"); !worked || err != nil {
		t.Fatalf("apply %v %v", worked, err)
	}
	var state string
	if err := f.p.QueryRow(ctx, "SELECT status FROM dns_change_plans WHERE id=$1", p.ID).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("state=%s %v", state, err)
	}
	tx, err := f.a.UserTx(ctx, f.tenant, f.user)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.New(tx).RequestDNSRollback(ctx, store.RequestDNSRollbackParams{TenantID: f.tenant, ID: p.ID, Reason: "Tenant reviewed the exact rollback changes"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.s.DNSOnce(ctx, "rollback-worker"); !worked || err != nil {
		t.Fatalf("rollback %v %v", worked, err)
	}
	if len(f.upstream.records) != 2 || f.upstream.records[0].ID != "website" || f.upstream.records[0].Value != "192.0.2.55" || f.upstream.records[1].Value != "v=spf1 include:thirdparty.example ~all" {
		t.Fatalf("foreign website/SPF lost %+v", f.upstream.records)
	}
}
func TestUnknownDNSResultCannotReplayOrClaimForeignOwnership(t *testing.T) {
	f := newFixture(t)
	p := f.plan(t)
	f.upstream.unknown = true
	ctx := context.Background()
	if worked, err := f.s.DNSOnce(ctx, "worker"); !worked || err == nil {
		t.Fatalf("unknown result=%v %v", worked, err)
	}
	writes := f.upstream.writes
	if worked, err := f.s.DNSOnce(ctx, "new-worker"); worked || err != nil || f.upstream.writes != writes {
		t.Fatalf("uncertain replay=%v %v writes=%d", worked, err, f.upstream.writes)
	}
	var state string
	if err := f.p.QueryRow(ctx, "SELECT status FROM dns_change_plans WHERE id=$1", p.ID).Scan(&state); err != nil || state != "uncertain" {
		t.Fatalf("state=%s %v", state, err)
	}
	var held bool
	if err := f.p.QueryRow(ctx, "SELECT bool_and(status IN('pending','uncertain') AND NOT owned) FROM dns_record_actions WHERE plan_id=$1", p.ID).Scan(&held); err != nil || !held {
		t.Fatalf("uncertain ownership fabricated %v %v", held, err)
	}
}

type readyAgent struct {
	trusted, ready bool
	generation     string
	issued         int
}

func (a *readyAgent) Do(_ context.Context, op string, _ any) (*agentd.Response, error) {
	var raw []byte
	switch op {
	case agentd.OpCertificateStatus:
		raw, _ = json.Marshal(agentd.CertificateStatus{Domain: "mail.platform.test", Trusted: a.trusted, ExpiresAt: time.Now().Add(24 * time.Hour)})
	case agentd.OpCertificateIssue:
		a.issued++
		return nil, errors.New("public ACME unavailable")
	case agentd.OpConfigStatus:
		raw, _ = json.Marshal(agentd.ApplyStatus{Ready: a.ready, Desired: a.generation, Applied: a.generation})
	default:
		return nil, errors.New("unexpected op")
	}
	return &agentd.Response{OK: true, Stdout: string(raw)}, nil
}

type readyResolver struct {
	records   []dnsprovider.Record
	missingMX bool
}

func (r readyResolver) LookupMX(context.Context, string) ([]*net.MX, error) {
	if r.missingMX {
		return nil, errors.New("missing")
	}
	return []*net.MX{{Host: "mail.platform.test.", Pref: 10}}, nil
}
func (r readyResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	var rows []string
	for _, v := range r.records {
		if v.Type == "TXT" && v.Name == name {
			rows = append(rows, v.Value)
		}
	}
	return rows, nil
}
func (r readyResolver) LookupCNAME(context.Context, string) (string, error) {
	return "mail.platform.test.", nil
}
func (r readyResolver) LookupNS(context.Context, string) ([]*net.NS, error) { return nil, nil }
func TestOnboardingRequiresDNSPublicCAAndAppliedProtocolGeneration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	d, err := store.New(f.p.Pool).PlatformDNSDomain(ctx, store.PlatformDNSDomainParams{TenantID: f.tenant, ID: f.domain})
	if err != nil {
		t.Fatal(err)
	}
	_, records, err := DomainDigest(d, f.s.Hostname)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = f.p.QueryRow(ctx, "INSERT INTO onboarding_tasks(tenant_id,domain_id,step,max_attempts) VALUES($1,$2,'dns_propagation',12) RETURNING id", f.tenant, f.domain).Scan(&id); err != nil {
		t.Fatal(err)
	}
	a := &readyAgent{generation: "fresh-generation", ready: true}
	r := readyResolver{records: records, missingMX: true}
	w := NewWorker(WorkerOptions{Service: f.s, Agent: a, Resolver: r, Owner: "worker", Apply: func(context.Context, string) (string, error) { return "fresh-generation", nil }})
	if _, err = w.Once(ctx); err != nil {
		t.Fatal(err)
	} // Bootstrap precedes any DNS or certificate attempt.
	if _, err = w.Once(ctx); err == nil {
		t.Fatal("missing MX was treated as propagated")
	}
	if a.issued != 0 {
		t.Fatal("certificate issued before DNS")
	}
	if _, err = f.p.Exec(ctx, "UPDATE onboarding_tasks SET next_attempt_at=now() WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	w.opts.Resolver = readyResolver{records: records}
	if _, err = w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Once(ctx); err == nil || a.issued != 1 {
		t.Fatalf("selfsigned/absent CA accepted %v issued=%d", err, a.issued)
	}
	a.trusted = true
	if _, err = f.p.Exec(ctx, "UPDATE onboarding_tasks SET next_attempt_at=now() WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err = w.Once(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var status, provision string
	if err = f.p.QueryRow(ctx, "SELECT task.status,d.provisioning_state FROM onboarding_tasks task JOIN domains d ON d.id=task.domain_id WHERE task.id=$1", id).Scan(&status, &provision); err != nil || status != "completed" || provision != "ready" {
		t.Fatalf("status=%s domain=%s %v", status, provision, err)
	}
}

func TestCredentialRevocationFencesLateUpstreamResult(t *testing.T) {
	f := newFixture(t)
	p := f.plan(t)
	ctx := context.Background()
	f.upstream.beforeWrite = func() {
		tx, err := f.a.UserTx(ctx, f.tenant, f.user)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err = store.New(tx).RevokeDNSCredential(ctx, store.RevokeDNSCredentialParams{TenantID: f.tenant, DomainID: f.domain}); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if worked, err := f.s.DNSOnce(ctx, "stale-worker"); !worked || err == nil {
		t.Fatalf("late result accepted %v %v", worked, err)
	}
	if f.upstream.writes != 1 {
		t.Fatalf("revoked credentials continued writing %d", f.upstream.writes)
	}
	var ok bool
	if err := f.p.QueryRow(ctx, "SELECT status='uncertain' AND fence>1 AND lease_owner IS NULL FROM dns_change_plans WHERE id=$1", p.ID).Scan(&ok); err != nil || !ok {
		t.Fatalf("revocation lost fence %v %v", ok, err)
	}
	if err := f.p.QueryRow(ctx, "SELECT bool_and(NOT owned) FROM dns_record_actions WHERE plan_id=$1", p.ID).Scan(&ok); err != nil || !ok {
		t.Fatal("late result claimed ownership")
	}
}
func TestDNSCredentialCiphertextAndForeignPlansStayPrivate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.plan(t)
	tx, err := f.a.UserTx(ctx, f.tenant, f.user)
	if err != nil {
		t.Fatal(err)
	}
	var cipher string
	if err = tx.QueryRow(ctx, "SELECT encrypted_secret FROM dns_credentials WHERE tenant_id=$1", f.tenant).Scan(&cipher); err == nil {
		t.Fatal("app read encrypted credential")
	}
	tx.Rollback(ctx)
	tx, err = f.a.TenantTx(ctx, f.tenant+100)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM dns_change_plans WHERE tenant_id=$1", f.tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("foreign plans visible %d %v", count, err)
	}
	if err = store.New(tx).ConfirmDNSPlan(ctx, store.ConfirmDNSPlanParams{TenantID: f.tenant, ID: p.ID, SourceDigest: p.SourceDigest}); err == nil {
		t.Fatal("foreign plan confirmed")
	}
}
