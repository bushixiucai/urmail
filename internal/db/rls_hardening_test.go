package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
)

// TestRLSHardening proves the 0008 boundary: the tenant role can only see and
// mutate its own tickets; audit/reference data is read-only; tenant/user/role
// lifecycle and sessions belong to the platform role.
func TestRLSHardening(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()
	applyMigrations(t, url)

	platform, err := db.OpenPlatform(ctx, url)
	if err != nil {
		t.Fatalf("open platform pool: %v", err)
	}
	defer platform.Close()
	app, err := db.OpenApp(ctx, url)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer app.Close()

	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatalf("platform tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `TRUNCATE ticket_messages, tickets, audit_logs, channels, announcements,
		sessions, user_tenant_roles, platform_admins, users, mailboxes, domains, tenants, brands, settings CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	var brandID, tenantA, tenantB, userA, userB int64
	if err := tx.QueryRow(ctx, "INSERT INTO brands (slug, name) VALUES ('rls8', 'RLS8') RETURNING id").Scan(&brandID); err != nil {
		t.Fatalf("brand: %v", err)
	}
	if err := tx.QueryRow(ctx, "INSERT INTO tenants (brand_id, name, slug, status) VALUES ($1, 'A', 'rls8-a', 'active') RETURNING id", brandID).Scan(&tenantA); err != nil {
		t.Fatalf("tenant A: %v", err)
	}
	if err := tx.QueryRow(ctx, "INSERT INTO tenants (brand_id, name, slug, status) VALUES ($1, 'B', 'rls8-b', 'active') RETURNING id", brandID).Scan(&tenantB); err != nil {
		t.Fatalf("tenant B: %v", err)
	}
	if err := tx.QueryRow(ctx, "INSERT INTO users (email, password_hash) VALUES ('rls8-a@example.test', 'x') RETURNING id").Scan(&userA); err != nil {
		t.Fatalf("user A: %v", err)
	}
	if err := tx.QueryRow(ctx, "INSERT INTO users (email, password_hash) VALUES ('rls8-b@example.test', 'x') RETURNING id").Scan(&userB); err != nil {
		t.Fatalf("user B: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO user_tenant_roles (user_id, tenant_id, role) VALUES ($1,$2,'owner'),($3,$4,'owner')", userA, tenantA, userB, tenantB); err != nil {
		t.Fatalf("roles: %v", err)
	}
	var ticketA, ticketB int64
	if err := tx.QueryRow(ctx, "INSERT INTO tickets (tenant_id, opened_by, subject) VALUES ($1,$2,'a') RETURNING id", tenantA, userA).Scan(&ticketA); err != nil {
		t.Fatalf("ticket A: %v", err)
	}
	if err := tx.QueryRow(ctx, "INSERT INTO tickets (tenant_id, opened_by, subject) VALUES ($1,$2,'b') RETURNING id", tenantB, userB).Scan(&ticketB); err != nil {
		t.Fatalf("ticket B: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO ticket_messages (tenant_id, ticket_id, author_id, body) VALUES ($1,$2,$3,'hello')", tenantA, ticketA, userA); err != nil {
		t.Fatalf("ticket message: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO audit_logs (tenant_id, action) VALUES ($1,'a'),($2,'b')", tenantA, tenantB); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO channels (brand_id, code, name) VALUES (NULL,'global-channel','Global'),($1,'brand-channel','Brand')", brandID); err != nil {
		t.Fatalf("channels: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO announcements (brand_id, title, body) VALUES (NULL,jsonb_build_object('en','g'),jsonb_build_object('en','g')),($1,jsonb_build_object('en','b'),jsonb_build_object('en','b'))", brandID); err != nil {
		t.Fatalf("announcements: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO sessions (id,user_id,tenant_id,csrf_token,expires_at) VALUES ('rls8-a',$1,$2,'c',now()+interval '1 day'),('rls8-b',$3,$4,'c',now()+interval '1 day')", userA, tenantA, userB, tenantB); err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE announcements SET state='published',published_at=now()"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	withTenant := func(t *testing.T, fn func(pgx.Tx)) {
		t.Helper()
		tx, err := app.UserTx(ctx, tenantA, userA)
		if err != nil {
			t.Fatalf("tenant tx: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		fn(tx)
	}
	count := func(t *testing.T, tx pgx.Tx, sql string, args ...any) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	expectRejected := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		tx, err := app.UserTx(ctx, tenantA, userA)
		if err != nil {
			t.Fatalf("tenant tx: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, sql, args...); err == nil {
			t.Fatalf("%q must be rejected", sql)
		}
	}

	t.Run("tickets are tenant scoped", func(t *testing.T) {
		withTenant(t, func(tx pgx.Tx) {
			if got := count(t, tx, "SELECT count(*) FROM tickets"); got != 1 {
				t.Fatalf("tickets visible = %d, want 1", got)
			}

		})
		expectRejected(t, "DELETE FROM tickets WHERE tenant_id=$1", tenantB)
		expectRejected(t, "INSERT INTO tickets (tenant_id, opened_by, subject) VALUES ($1,$2,'x')", tenantB, userB)
		expectRejected(t, "INSERT INTO ticket_messages (tenant_id, ticket_id, author_id, body) VALUES ($1,$2,$3,'x')", tenantA, ticketB, userA)
	})

	t.Run("audit and reference data are read-only", func(t *testing.T) {
		withTenant(t, func(tx pgx.Tx) {
			if got := count(t, tx, "SELECT count(*) FROM audit_logs"); got != 1 {
				t.Fatalf("audit visible = %d, want 1", got)
			}
			if got := count(t, tx, "SELECT count(*) FROM channels"); got != 2 {
				t.Fatalf("channels visible = %d, want 2", got)
			}
			if got := count(t, tx, "SELECT count(*) FROM announcements"); got != 2 {
				t.Fatalf("announcements visible = %d, want 2", got)
			}
		})
		expectRejected(t, "DELETE FROM audit_logs")
		expectRejected(t, "INSERT INTO audit_logs (tenant_id, action) VALUES ($1,'forged')", tenantA)
		expectRejected(t, "DELETE FROM channels")
		expectRejected(t, "DELETE FROM announcements")
		expectRejected(t, "INSERT INTO channels (code,name) VALUES ('x','x')")
	})

	t.Run("users roles tenants and sessions are platform only", func(t *testing.T) {
		expectRejected(t, "UPDATE users SET password_hash='pwned' WHERE id=$1", userA)
		expectRejected(t, "DELETE FROM users WHERE id=$1", userA)
		expectRejected(t, "UPDATE user_tenant_roles SET role='owner' WHERE user_id=$1", userA)
		expectRejected(t, "UPDATE tenants SET status='closed' WHERE id=$1", tenantA)
		expectRejected(t, "DELETE FROM sessions")
		expectRejected(t, "SELECT count(*) FROM sessions")
	})
}
