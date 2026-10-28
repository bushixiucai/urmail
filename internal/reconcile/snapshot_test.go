package reconcile_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/mail"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/store"
)

type mockQuerier struct {
	domains      []store.PlatformListDomainsRow
	spamPolicies []store.PlatformListSpamDeliveryPoliciesRow
	transport    []store.PlatformListTransportSecurityRow
}

func (m *mockQuerier) PlatformListTransportSecurity(context.Context) ([]store.PlatformListTransportSecurityRow, error) {
	return m.transport, nil
}

func (m *mockQuerier) PlatformListDNSSecurity(context.Context) ([]store.PlatformListDNSSecurityRow, error) {
	return nil, nil
}

func (m *mockQuerier) PlatformListSpamDeliveryPolicies(context.Context) ([]store.PlatformListSpamDeliveryPoliciesRow, error) {
	return m.spamPolicies, nil
}

func (m *mockQuerier) PlatformListManagedDNS(context.Context) ([]store.PlatformListManagedDNSRow, error) {
	return nil, nil
}

func (m *mockQuerier) PlatformListDomains(_ context.Context) ([]store.PlatformListDomainsRow, error) {
	if m.domains != nil {
		return m.domains, nil
	}
	return []store.PlatformListDomainsRow{
		{Fqdn: "example.com", Status: "active"},
	}, nil
}

func (m *mockQuerier) PlatformListMailboxes(_ context.Context) ([]store.PlatformListMailboxesRow, error) {
	return []store.PlatformListMailboxesRow{
		{Address: "user@example.com", Status: "active"},
	}, nil
}

func (m *mockQuerier) PlatformListAliases(_ context.Context) ([]store.PlatformListAliasesRow, error) {
	return []store.PlatformListAliasesRow{
		{Domain: "example.com", LocalPart: "info", Target: "user@example.com", Kind: "alias"},
	}, nil
}

func (m *mockQuerier) PlatformListDKIMDomains(_ context.Context) ([]store.PlatformListDKIMDomainsRow, error) {
	return nil, nil
}

func TestAssembleConfigContextAndRender(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		MyHostname:    "box.urmail.cn",
		PrimaryDomain: "urmail.cn",
		StorageRoot:   "/var/mail",
		DatabaseURL:   "host=127.0.0.1 dbname=urmail user=urmail_dovecot",
		ListenAddr:    "http://127.0.0.1:8080",
	}

	mailCtx, err := reconcile.AssembleConfigContext(ctx, cfg, &mockQuerier{}, "1.2.3.4", []string{"ns1.urmail.cn"})
	if err != nil {
		t.Fatalf("AssembleConfigContext failed: %v", err)
	}

	if len(mailCtx.PostfixSnap.Domains) != 1 {
		t.Errorf("expected 1 postfix domain, got %d", len(mailCtx.PostfixSnap.Domains))
	}
	if len(mailCtx.PostfixSnap.Mailboxes) != 1 {
		t.Errorf("expected 1 postfix mailbox, got %d", len(mailCtx.PostfixSnap.Mailboxes))
	}
	if len(mailCtx.NSDSnap.Zones) != 1 {
		t.Errorf("expected 1 nsd zone, got %d", len(mailCtx.NSDSnap.Zones))
	}

	// Verify that the assembled context can directly feed M2 RenderAll
	files, err := mail.RenderAll(*mailCtx)
	if err != nil {
		t.Fatalf("RenderAll with assembled context failed: %v", err)
	}

	if _, ok := files["/etc/postfix/main.cf"]; !ok {
		t.Error("missing /etc/postfix/main.cf in rendered output")
	}
	if _, ok := files["/etc/dovecot/conf.d/urmail/dovecot.conf"]; !ok {
		t.Error("missing /etc/dovecot/conf.d/urmail/dovecot.conf in rendered output")
	}
}

func TestZoneSerialFormat(t *testing.T) {
	s := reconcile.ZoneSerial(time.Date(2026, 10, 22, 8, 40, 0, 0, time.UTC))
	// 8:40 => bucket (8*60+40)/15 = 34 => +1 = 35
	if s != 2026102235 {
		t.Errorf("expected 2026102235, got %d", s)
	}
	// End of day clamps at bucket 96
	e := reconcile.ZoneSerial(time.Date(2026, 10, 22, 23, 59, 0, 0, time.UTC))
	if e != 2026102296 {
		t.Errorf("expected 2026102296, got %d", e)
	}
	// Strictly ordered within a day
	//nolint:staticcheck // The positive ordering assertion is easier to audit as written.
	if !(reconcile.ZoneSerial(time.Date(2026, 10, 22, 0, 1, 0, 0, time.UTC)) <
		reconcile.ZoneSerial(time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)) &&
		reconcile.ZoneSerial(time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)) < e) {
		t.Error("serials must increase within a day")
	}
}

func TestAssembleSkipsDelegatedZones(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		MyHostname:    "box.urmail.cn",
		PrimaryDomain: "urmail.cn",
		StorageRoot:   "/var/mail",
	}

	q := &mockQuerier{
		domains: []store.PlatformListDomainsRow{
			{Fqdn: "managed.example.com", Status: "active", Mode: "managed", UpdatedAt: pgtype.Timestamptz{Time: time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC), Valid: true}},
			{Fqdn: "delegated.example.com", Status: "active", Mode: "delegated"},
		},
	}

	mailCtx, err := reconcile.AssembleConfigContext(ctx, cfg, q, "1.2.3.4", nil)
	if err != nil {
		t.Fatalf("AssembleConfigContext: %v", err)
	}
	if len(mailCtx.NSDSnap.Zones) != 1 {
		t.Fatalf("expected 1 nsd zone (delegated skipped), got %d", len(mailCtx.NSDSnap.Zones))
	}
	z := mailCtx.NSDSnap.Zones[0]
	if z.Domain != "managed.example.com" {
		t.Errorf("unexpected zone domain %q", z.Domain)
	}
	if z.Serial != 2026102237 {
		t.Errorf("expected serial derived from updated_at (2026102237), got %d", z.Serial)
	}
	// postfix still sees both domains
	if len(mailCtx.PostfixSnap.Domains) != 2 {
		t.Errorf("postfix must still see all active domains, got %d", len(mailCtx.PostfixSnap.Domains))
	}
}

func TestSpamDeliveryPolicyChangesRenderedGeneration(t *testing.T) {
	cfg := &config.Config{MyHostname: "box.urmail.cn", PrimaryDomain: "urmail.cn", StorageRoot: "/var/mail", DatabaseURL: "host=127.0.0.1 user=urmail_dovecot", ListenAddr: "127.0.0.1:8080"}
	q := &mockQuerier{}
	before, err := reconcile.AssembleConfigContext(t.Context(), cfg, q, "192.0.2.1", nil)
	if err != nil {
		t.Fatal(err)
	}
	oldFiles, err := mail.RenderAll(*before)
	if err != nil {
		t.Fatal(err)
	}
	q.spamPolicies = []store.PlatformListSpamDeliveryPoliciesRow{{MailboxAddress: "user@example.com", Scope: "mailbox", SenderAddress: "sender@vendor.test", List: "deny"}}
	after, err := reconcile.AssembleConfigContext(t.Context(), cfg, q, "192.0.2.1", nil)
	if err != nil {
		t.Fatal(err)
	}
	newFiles, err := mail.RenderAll(*after)
	if err != nil {
		t.Fatal(err)
	}
	const key = "/etc/dovecot/urmail-spam/spam-before.sieve"
	if oldFiles[key] == newFiles[key] {
		t.Fatal("private feedback did not change rendered generation")
	}
	if len(after.DovecotNode.SpamPolicies) != 1 {
		t.Fatal("private policy not in authoritative snapshot")
	}
}
