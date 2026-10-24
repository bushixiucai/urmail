package testdb_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestDatabaseIsolationAndOwnedCleanup(t *testing.T) {
	dsn := os.Getenv("URMAIL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("URMAIL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	observer, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observer.Close(ctx) }()
	var before bool
	if err = observer.QueryRow(ctx, "SELECT to_regclass('public.fixture_owned_probe') IS NOT NULL").Scan(&before); err != nil {
		t.Fatal(err)
	}
	var name string
	t.Run("owned", func(t *testing.T) {
		target := testdb.New(t)
		parsed, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		name = parsed.Path[1:]
		if target == dsn {
			t.Fatal("fixture returned its configured database instead of an owned one")
		}
		owned, err := pgx.Connect(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = owned.Close(ctx) })
		if _, err = owned.Exec(ctx, "CREATE TABLE fixture_owned_probe(value integer)"); err != nil {
			t.Fatal(err)
		}
		var after bool
		if err = observer.QueryRow(ctx, "SELECT to_regclass('public.fixture_owned_probe') IS NOT NULL").Scan(&after); err != nil || after != before {
			t.Fatalf("owned schema escaped to configured database: before=%t after=%t err=%v", before, after, err)
		}
	})
	var exists bool
	if err = observer.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", name).Scan(&exists); err != nil || exists {
		t.Fatalf("owned database was not cleaned: exists=%t err=%v", exists, err)
	}
}

func TestParallelDatabaseFixtures(t *testing.T) {
	for i := range 4 {
		t.Run(fmt.Sprintf("fixture-%d", i), func(t *testing.T) {
			t.Parallel()
			dsn := testdb.New(t)
			ctx := context.Background()
			conn, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close(ctx) })
			if _, err = conn.Exec(ctx, "CREATE TABLE parallel_fixture_probe(value integer)"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
