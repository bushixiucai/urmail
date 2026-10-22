package reconcile_test

import (
	"context"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/mail"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/store"
)

type mockQuerier struct{}

func (m *mockQuerier) PlatformListDomains(ctx context.Context) ([]store.PlatformListDomainsRow, error) {
	return []store.PlatformListDomainsRow{
		{Fqdn: "example.com", Status: "active"},
	}, nil
}

func (m *mockQuerier) PlatformListMailboxes(ctx context.Context) ([]store.PlatformListMailboxesRow, error) {
	return []store.PlatformListMailboxesRow{
		{Address: "user@example.com", Status: "active"},
	}, nil
}

func (m *mockQuerier) PlatformListAliases(ctx context.Context) ([]store.PlatformListAliasesRow, error) {
	return []store.PlatformListAliasesRow{
		{Domain: "example.com", LocalPart: "info", Target: "user@example.com", Kind: "alias"},
	}, nil
}

func TestAssembleConfigContextAndRender(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		MyHostname:    "box.urmail.cn",
		PrimaryDomain: "urmail.cn",
		StorageRoot:   "/var/mail",
		DatabaseURL:   "host=127.0.0.1 dbname=urmail user=urmail_dovecot",
		ListenAddr:    "127.0.0.1:8080",
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
