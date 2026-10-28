package brandhost

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestBrandMailHostnameNamespacePG(t *testing.T) {
	p, _ := testdb.OpenMigrated(t)
	ctx := t.Context()
	q := store.New(p)
	var brand, tenant int64
	if e := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('host-space','Host space') RETURNING id").Scan(&brand); e != nil {
		t.Fatal(e)
	}
	if e := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug) VALUES($1,'Namespace','host-space') RETURNING id", brand).Scan(&tenant); e != nil {
		t.Fatal(e)
	}
	domain := func(host string) error {
		_, e := p.Exec(ctx, "INSERT INTO domains(tenant_id,fqdn,verification_token) VALUES($1,$2,'fixture')", tenant, host)
		return e
	}
	bind := func(host string) error {
		_, e := q.PlatformBindBrandHost(ctx, store.PlatformBindBrandHostParams{Hostname: host, ID: brand})
		return e
	}
	conflict := func(e error) {
		t.Helper()
		var pg *pgconn.PgError
		if !errors.As(e, &pg) || pg.Code != "23505" {
			t.Fatalf("expected authoritative namespace conflict, got %v", e)
		}
	}
	if e := bind("brand-first.test"); e != nil {
		t.Fatal(e)
	}
	conflict(domain("brand-first.test"))
	conflict(domain("BRAND-FIRST.TEST."))
	if _, e := q.PlatformUnbindBrandHost(ctx, "brand-first.test"); e != nil {
		t.Fatal(e)
	}
	conflict(domain("brand-first.test"))
	if e := domain("mail-first.test"); e != nil {
		t.Fatal(e)
	}
	conflict(bind("mail-first.test"))
	conflict(bind("MAIL-FIRST.TEST."))
	var wg sync.WaitGroup
	out := make(chan error, 2)
	start := make(chan struct{})
	for _, f := range []func(string) error{domain, bind} {
		wg.Add(1)
		go func(call func(string) error) { defer wg.Done(); <-start; out <- call("concurrent-host.test") }(f)
	}
	close(start)
	wg.Wait()
	close(out)
	success, failures := 0, 0
	for e := range out {
		if e == nil {
			success++
		} else {
			conflict(e)
			failures++
		}
	}
	if success != 1 || failures != 1 {
		t.Fatal("concurrent ownership was not exclusive", success, failures)
	}
	if _, e := p.Exec(ctx, "DELETE FROM domains WHERE fqdn='concurrent-host.test'"); e != nil {
		t.Fatal(e)
	}
	// Down removes only derived guards; a populated historical collision then
	// causes the forward migration to fail transactionally and diagnostically.

	down, e := fs.ReadFile(db.FS(), "0148_brand_mail_host_namespace.down.sql")
	if e != nil {
		t.Fatal(e)
	}
	up, e := fs.ReadFile(db.FS(), "0148_brand_mail_host_namespace.up.sql")
	if e != nil {
		t.Fatal(e)
	}
	tx, e := p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(ctx, string(down)); e != nil {
		t.Fatal("down148", e)
	}
	if _, e = tx.Exec(ctx, "INSERT INTO domains(tenant_id,fqdn,verification_token) VALUES($1,'brand-first.test','fixture')", tenant); e != nil {
		t.Fatal("fixture historical collision", e)
	}
	if _, e = tx.Exec(ctx, string(up)); e == nil || !strings.Contains(e.Error(), "existing brand/mail hostname collision") {
		t.Fatal("historical collision did not fail diagnostically", e)
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	conflict(domain("brand-first.test"))
}
