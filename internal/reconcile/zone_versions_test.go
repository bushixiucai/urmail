package reconcile_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestPersistentZoneVersionsAreAtomicAndWrap(t *testing.T) {
	url := testdb.New(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("dns_review_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE dns_zone_versions(domain text PRIMARY KEY,content_hash text NOT NULL,serial bigint NOT NULL CHECK(serial>=0 AND serial<4294967296),updated_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	versions := &reconcile.VersionedStore{DB: pool}
	a := strings.Repeat("a", 64)
	b := strings.Repeat("b", 64)
	serial, err := versions.ReserveZoneSerial(ctx, "example.com", a, 2026102300)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan uint32, 20)
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := versions.ReserveZoneSerial(ctx, "example.com", a, 1)
			results <- s
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for s := range results {
		if s != serial {
			t.Fatalf("unchanged concurrent content advanced serial %d != %d", s, serial)
		}
	}
	advanced, err := versions.ReserveZoneSerial(ctx, "example.com", b, 1)
	if err != nil || advanced != serial+1 {
		t.Fatalf("changed content did not advance: %d %v", advanced, err)
	}
	restored, err := versions.ReserveZoneSerial(ctx, "example.com", a, 1)
	if err != nil || restored != advanced+1 {
		t.Fatalf("rollback did not get new serial: %d %v", restored, err)
	}
	maximum, err := versions.ReserveZoneSerial(ctx, "wrap.example.com", a, 4294967294)
	if err != nil || maximum != 4294967295 {
		t.Fatalf("initial wrap boundary: %d %v", maximum, err)
	}
	wrapped, err := versions.ReserveZoneSerial(ctx, "wrap.example.com", b, 0)
	if err != nil || wrapped != 0 {
		t.Fatalf("RFC serial rollover: %d %v", wrapped, err)
	}
}
