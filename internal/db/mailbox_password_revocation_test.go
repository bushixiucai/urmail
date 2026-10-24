package db_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

type mailboxPasswordFixture struct {
	dsn                                string
	platform                           *db.Pool
	tenant, otherTenant, user, mailbox int64
	address, session                   string
}

func newMailboxPasswordFixture(t *testing.T) mailboxPasswordFixture {
	t.Helper()
	dsn := testDatabaseURL(t)
	applyMigrations(t, dsn)
	p, err := db.OpenPlatform(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := p.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('password-revocation','Password revocation') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Password owner','password-owner','active') RETURNING id", brand)
	other := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Password other','password-other','active') RETURNING id", brand)
	user := scalar("INSERT INTO users(email,password_hash) VALUES('password-owner@example.test','unused') RETURNING id")
	if _, err = p.Exec(t.Context(), "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", user, tenant); err != nil {
		t.Fatal(err)
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,provisioning_state) VALUES($1,'password.example.test','active','fixture',now(),'ready') RETURNING id", tenant)
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,'self','self@password.example.test','fixture-hash-a',1024) RETURNING id", tenant, domain)
	token := strings.Repeat("a", 64)
	if _, err = store.New(p.Pool).PlatformCreateMailboxSession(t.Context(), store.PlatformCreateMailboxSessionParams{TokenHash: token, Address: "self@password.example.test"}); err != nil {
		t.Fatal(err)
	}
	return mailboxPasswordFixture{dsn, p, tenant, other, user, mailbox, "self@password.example.test", token}
}

func (f mailboxPasswordFixture) state(t *testing.T) (string, int64, bool) {
	t.Helper()
	var hash string
	var version int64
	var pending bool
	if err := f.platform.QueryRow(t.Context(), "SELECT password_hash,password_version,password_revoke_pending FROM mailboxes WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox).Scan(&hash, &version, &pending); err != nil {
		t.Fatal(err)
	}
	return hash, version, pending
}

func TestMailboxPasswordVersionApplicationCannotForgeOrAcknowledge(t *testing.T) {
	f := newMailboxPasswordFixture(t)
	if _, err := f.platform.Exec(t.Context(), "UPDATE mailboxes SET password_hash='fixture-hash-b' WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox); err != nil {
		t.Fatal(err)
	}
	root, err := pgx.Connect(t.Context(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close(context.Background()) }()
	for _, query := range []string{"UPDATE mailboxes SET password_version=password_version+1 WHERE tenant_id=$1 AND id=$2", "UPDATE mailboxes SET password_revoke_pending=false WHERE tenant_id=$1 AND id=$2"} {
		tx, beginErr := root.Begin(t.Context())
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if _, err = tx.Exec(t.Context(), "SET LOCAL ROLE urmail_app"); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(t.Context(), "SELECT set_config('urmail.tenant_id',$1,true),set_config('urmail.user_id',$2,true)", strconv.FormatInt(f.tenant, 10), strconv.FormatInt(f.user, 10)); err != nil {
			t.Fatal(err)
		}
		var role string
		var own int
		if err = tx.QueryRow(t.Context(), "SELECT current_user,(SELECT count(*) FROM mailboxes WHERE tenant_id=$1 AND id=$2)", f.tenant, f.mailbox).Scan(&role, &own); err != nil || role != "urmail_app" || own != 1 {
			t.Fatalf("application scope fixture invalid: role=%s visible=%d err=%v", role, own, err)
		}
		_, err = tx.Exec(t.Context(), query, f.tenant, f.mailbox)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "42501" {
			t.Fatalf("application forged protected password lifecycle state: %v", err)
		}
		_ = tx.Rollback(t.Context())
	}
	hash, version, pending := f.state(t)
	if hash != "fixture-hash-b" || version != 1 || !pending {
		t.Fatalf("denied application writes changed state: version=%d pending=%t", version, pending)
	}
}

func TestMailboxPasswordVersionCASAndAcknowledgmentIdentity(t *testing.T) {
	f := newMailboxPasswordFixture(t)
	q := store.New(f.platform.Pool)
	base := store.PlatformChangeMailboxPasswordCASParams{TenantID: f.tenant, MailboxID: f.mailbox, Address: f.address, PasswordHash: "fixture-hash-b", ExpectedHash: "fixture-hash-a", ExpectedVersion: 0}
	for _, bad := range []string{"tenant", "mailbox", "address", "hash", "version"} {
		arg := base
		switch bad {
		case "tenant":
			arg.TenantID = f.otherTenant
		case "mailbox":
			arg.MailboxID++
		case "address":
			arg.Address = "other@password.example.test"
		case "hash":
			arg.ExpectedHash = "wrong"
		case "version":
			arg.ExpectedVersion++
		}
		n, err := q.PlatformChangeMailboxPasswordCAS(t.Context(), arg)
		if err != nil || n != 0 {
			t.Fatalf("invalid %s CAS changed mailbox: rows=%d err=%v", bad, n, err)
		}
	}
	if n, err := q.PlatformChangeMailboxPasswordCAS(t.Context(), base); err != nil || n != 1 {
		t.Fatalf("correct CAS rows=%d err=%v", n, err)
	}
	if n, err := q.PlatformChangeMailboxPasswordCAS(t.Context(), base); err != nil || n != 0 {
		t.Fatalf("replayed CAS changed mailbox: rows=%d err=%v", n, err)
	}
	ack := store.PlatformAckMailboxPasswordRevocationParams{TenantID: f.tenant, MailboxID: f.mailbox, Address: f.address, ExpectedVersion: 1}
	for _, bad := range []string{"tenant", "mailbox", "address", "version"} {
		arg := ack
		switch bad {
		case "tenant":
			arg.TenantID = f.otherTenant
		case "mailbox":
			arg.MailboxID++
		case "address":
			arg.Address = "other@password.example.test"
		case "version":
			arg.ExpectedVersion--
		}
		n, err := q.PlatformAckMailboxPasswordRevocation(t.Context(), arg)
		if err != nil || n != 0 {
			t.Fatalf("invalid %s ACK cleared pending: rows=%d err=%v", bad, n, err)
		}
	}
	if _, version, pending := f.state(t); version != 1 || !pending {
		t.Fatalf("mismatched ACK changed pending version=%d pending=%t", version, pending)
	}
	if n, err := q.PlatformAckMailboxPasswordRevocation(t.Context(), ack); err != nil || n != 1 {
		t.Fatalf("correct ACK rows=%d err=%v", n, err)
	}
	if n, err := q.PlatformAckMailboxPasswordRevocation(t.Context(), ack); err != nil || n != 0 {
		t.Fatalf("duplicate ACK rows=%d err=%v", n, err)
	}
	// Both callers observed the same authenticated hash/version. PostgreSQL
	// must allow exactly one winner after the row lock is acquired.
	type result struct {
		rows int64
		err  error
	}
	results := make(chan result, 2)
	for _, nextHash := range []string{"fixture-hash-c", "fixture-hash-d"} {
		go func(hash string) {
			rows, updateErr := q.PlatformChangeMailboxPasswordCAS(t.Context(), store.PlatformChangeMailboxPasswordCASParams{TenantID: f.tenant, MailboxID: f.mailbox, Address: f.address, ExpectedHash: "fixture-hash-b", ExpectedVersion: 1, PasswordHash: hash})
			results <- result{rows, updateErr}
		}(nextHash)
	}
	var winners int64
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		winners += got.rows
	}
	if _, version, pending := f.state(t); winners != 1 || version != 2 || !pending {
		t.Fatalf("concurrent CAS lost serialization: winners=%d version=%d pending=%t", winners, version, pending)
	}
}

func TestMailboxPasswordVersionABARetainsPermanentSessionRevocation(t *testing.T) {
	f := newMailboxPasswordFixture(t)
	q := store.New(f.platform.Pool)
	if _, err := q.PlatformCreateMailboxTicket(t.Context(), store.PlatformCreateMailboxTicketParams{TokenHash: strings.Repeat("c", 64), SessionHash: f.session, Scope: "sieve"}); err != nil {
		t.Fatal(err)
	}
	if n, err := q.PlatformChangeMailboxPasswordCAS(t.Context(), store.PlatformChangeMailboxPasswordCASParams{TenantID: f.tenant, MailboxID: f.mailbox, Address: f.address, ExpectedHash: "fixture-hash-a", ExpectedVersion: 0, PasswordHash: "fixture-hash-b"}); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	second := strings.Repeat("b", 64)
	if _, err := q.PlatformCreateMailboxSession(t.Context(), store.PlatformCreateMailboxSessionParams{TokenHash: second, Address: f.address}); err != nil {
		t.Fatal(err)
	}
	if n, err := q.PlatformChangeMailboxPasswordCAS(t.Context(), store.PlatformChangeMailboxPasswordCASParams{TenantID: f.tenant, MailboxID: f.mailbox, Address: f.address, ExpectedHash: "fixture-hash-b", ExpectedVersion: 1, PasswordHash: "fixture-hash-a"}); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := q.PlatformAckMailboxPasswordRevocation(t.Context(), store.PlatformAckMailboxPasswordRevocationParams{TenantID: f.tenant, MailboxID: f.mailbox, Address: f.address, ExpectedVersion: 1}); err != nil || n != 0 {
		t.Fatalf("stale acknowledgment cleared ABA pending: %d %v", n, err)
	}
	hash, version, pending := f.state(t)
	if hash != "fixture-hash-a" || version != 2 || !pending {
		t.Fatalf("ABA lifecycle lost: version=%d pending=%t", version, pending)
	}
	for _, token := range []string{f.session, second} {
		if _, err := q.PlatformGetMailboxSession(t.Context(), token); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("old session revived after hash ABA: %v", err)
		}
		if _, err := q.PlatformMailboxPasswordForSession(t.Context(), store.PlatformMailboxPasswordForSessionParams{SessionHash: token, Address: f.address}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("old password-change session revived: %v", err)
		}
	}
	if _, err := q.PlatformConsumeMailboxTicket(t.Context(), store.PlatformConsumeMailboxTicketParams{TokenHash: strings.Repeat("c", 64), Scope: "sieve"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("old operation ticket revived after hash ABA: %v", err)
	}
	if n, err := q.PlatformAckMailboxPasswordRevocation(t.Context(), store.PlatformAckMailboxPasswordRevocationParams{TenantID: f.tenant, MailboxID: f.mailbox, Address: f.address, ExpectedVersion: 2}); err != nil || n != 1 {
		t.Fatalf("current ABA acknowledgment rows=%d err=%v", n, err)
	}
}

func TestMailboxPasswordForSessionBindsIdentityAndLocksMailbox(t *testing.T) {
	f := newMailboxPasswordFixture(t)
	q := store.New(f.platform.Pool)
	for _, arg := range []store.PlatformMailboxPasswordForSessionParams{{SessionHash: f.session, Address: "other@password.example.test"}, {SessionHash: strings.Repeat("d", 64), Address: f.address}} {
		if _, err := q.PlatformMailboxPasswordForSession(t.Context(), arg); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("unbound session/address obtained password row: %v", err)
		}
	}
	tx, err := f.platform.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	row, err := store.New(tx).PlatformMailboxPasswordForSession(t.Context(), store.PlatformMailboxPasswordForSessionParams{SessionHash: f.session, Address: f.address})
	if err != nil || row.MailboxID != f.mailbox || row.TenantID != f.tenant || row.PasswordVersion != 0 {
		t.Fatalf("bound password row mismatch: %+v %v", row, err)
	}
	other, err := f.platform.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(t.Context()) }()
	_, err = other.Exec(t.Context(), "SELECT id FROM mailboxes WHERE tenant_id=$1 AND id=$2 FOR UPDATE NOWAIT", f.tenant, f.mailbox)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "55P03" {
		t.Fatalf("session lookup did not lock mailbox before CAS: %v", err)
	}
}

func TestMailboxPasswordVersionMigrationRollbackProtectsHistory(t *testing.T) {
	for _, kind := range []string{"pending-only", "acknowledged-history", "pristine"} {
		t.Run(kind, func(t *testing.T) {
			f := newMailboxPasswordFixture(t)
			m, err := db.NewMigrator(f.dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = m.Close() }()
			for {
				version, dirty, e := m.Version()
				if e != nil || dirty || version < 67 {
					t.Fatalf("invalid rollback fixture version=%d dirty=%t err=%v", version, dirty, e)
				}
				if version == 67 {
					break
				}
				if e = m.Down(t.Context()); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "pending-only" {
				_, err = f.platform.Exec(t.Context(), "UPDATE mailboxes SET password_revoke_pending=true WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox)
			}
			if kind == "acknowledged-history" {
				_, err = f.platform.Exec(t.Context(), "UPDATE mailboxes SET password_hash='fixture-history' WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox)
				if err == nil {
					_, err = store.New(f.platform.Pool).PlatformAckMailboxPasswordRevocation(t.Context(), store.PlatformAckMailboxPasswordRevocationParams{TenantID: f.tenant, MailboxID: f.mailbox, Address: f.address, ExpectedVersion: 1})
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			beforeHash, beforeVersion, beforePending := f.state(t)
			if (kind == "acknowledged-history" && (beforeVersion != 1 || beforePending)) || (kind == "pending-only" && (beforeVersion != 0 || !beforePending)) {
				t.Fatal("rollback fixture did not isolate pending from acknowledged version history")
			}
			err = m.Down(t.Context())
			if kind != "pristine" {
				if err == nil || !strings.Contains(err.Error(), "MAILBOX_PASSWORD_VERSIONS_REQUIRE_CONTROLLED_ROLLBACK") {
					t.Fatalf("rollback discarded credential history: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var fields int
				if err = f.platform.QueryRow(t.Context(), "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='mailboxes' AND column_name IN('password_version','password_revoke_pending')").Scan(&fields); err != nil || fields != 0 {
					t.Fatalf("clean down67 retained fields: %d %v", fields, err)
				}
				if err = m.Up(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			hash, version, pending := f.state(t)
			if hash != beforeHash || version != beforeVersion || pending != beforePending {
				t.Fatal("rollback guard/down-up changed credentials or durable revocation history")
			}
		})
	}
}
