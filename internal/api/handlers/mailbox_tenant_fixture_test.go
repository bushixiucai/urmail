package handlers

import (
	"context"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
)

// mailboxTenant is a tenant with one verified domain. Mailbox tests do not
// need a plan, subscription, or billing handler.
type mailboxTenant struct {
	p   *db.Pool
	tid int64
}

func newMailboxTenant(t *testing.T) mailboxTenant {
	t.Helper()
	p, _ := authIntegrationDatabase(t)
	ctx := context.Background()
	var brand int64
	s := mailboxTenant{p: p}
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('mailbox-fixture','Mailbox') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Mailbox','mailbox-fixture','active') RETURNING id", brand).Scan(&s.tid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'mailbox-fixture.test','active',now(),'fixture-proof')", s.tid); err != nil {
		t.Fatal(err)
	}
	return s
}
