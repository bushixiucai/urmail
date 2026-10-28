package agentd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestDNSAuthorityPlatformVisibilityOwnershipAndRevocationPG(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	ctx := t.Context()
	var brand, tenant, domain, owner int64
	seed := func(sql string, dest *int64, args ...any) {
		t.Helper()
		if err := platform.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	seed("INSERT INTO brands(slug,name) VALUES('dns-platform','DNS platform') RETURNING id", &brand)
	seed("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'DNS','dns-platform','active') RETURNING id", &tenant, brand)
	seed("INSERT INTO users(email,password_hash) VALUES('dns-authority-owner@example.test','unused') RETURNING id", &owner)
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", owner, tenant); err != nil {
		t.Fatal(err)
	}
	seed("INSERT INTO domains(tenant_id,fqdn,mode,status,verified_at,verification_token,provisioning_state) VALUES($1,'platform-dns.test','managed','active',now(),'fixture','ready') RETURNING id", &domain, tenant)
	var actualRole, setting string
	var bypass bool
	if err := platform.QueryRow(ctx, "SELECT current_user,rolbypassrls,coalesce(current_setting('urmail.tenant_id',true),'') FROM pg_roles WHERE rolname=current_user").Scan(&actualRole, &bypass, &setting); err != nil || actualRole != "urmail_platform" || !bypass || setting != "" {
		t.Fatalf("unexpected platform role/GUC %q bypass=%v tenant=%q err=%v", actualRole, bypass, setting, err)
	}
	q := store.New(platform)
	initial, err := q.PlatformGetDNSSecurity(ctx, "platform-dns.test")
	if err != nil || initial.OwnerTenantID != tenant || initial.RegisteredDomainID != domain {
		t.Fatalf("registered ownership %+v %v", initial, err)
	}
	_, err = q.PlatformSetDNSSecurity(ctx, store.PlatformSetDNSSecurityParams{Fqdn: "platform-dns.test", TenantID: tenant, ID: domain, DnssecEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := q.PlatformListDNSSecurity(ctx)
	if err != nil || len(rows) != 1 || !rows[0].DnssecEnabled {
		t.Fatalf("unscoped platform list %+v %v", rows, err)
	}
	current, err := q.PlatformGetDNSSecurity(ctx, "platform-dns.test")
	if err != nil || !current.DnssecEnabled.Bool {
		t.Fatalf("unscoped platform get %+v %v", current, err)
	}
	var count int
	if err = app.QueryRow(ctx, "SELECT count(*) FROM domain_dns_security").Scan(&count); err != nil || count != 0 {
		t.Fatalf("app without tenant saw DNS intent count=%d err=%v", count, err)
	}
	manager := &ApplyManager{pool: platform, cfg: &config.Config{}, root: t.TempDir()}
	if _, err = manager.authorizedDNS(ctx, "platform-dns.test", tenant, domain+1, owner); err == nil {
		t.Fatal("recreated or foreign domain identity accepted")
	}
	if _, err = manager.authorizedDNS(ctx, "platform-dns.test", tenant, domain, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Exec(ctx, "UPDATE user_tenant_roles SET role='member' WHERE tenant_id=$1 AND user_id=$2", tenant, owner); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(DNSAuthorityParams{TenantID: tenant, DomainID: domain, ActorID: owner, Domain: "platform-dns.test", Action: "prepare"})
	if _, _, err = manager.ManageDNSAuthority(params); err == nil || !strings.Contains(err.Error(), "role revoked") {
		t.Fatalf("revoked actor reached privileged mutation: %v", err)
	}
	entries, err := os.ReadDir(manager.root)
	if err != nil || len(entries) != 0 {
		t.Fatal("revoked actor changed root key state", err)
	}
}
