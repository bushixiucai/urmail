package aliassenders

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

type maintenanceFixture struct {
	pool   *db.Pool
	intent Intent
	other  int64
}

func newMaintenanceFixture(t *testing.T) maintenanceFixture {
	t.Helper()
	dsn := testdb.New(t)
	m, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	pool, err := db.OpenPlatform(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	scalar := func(q string, args ...any) int64 {
		t.Helper()
		var n int64
		if e := pool.QueryRow(t.Context(), q, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('sender-maintenance','Sender maintenance') RETURNING id")
	actor := scalar("INSERT INTO users(email,password_hash) VALUES('operator@sender.test','fixture') RETURNING id")
	if _, err = pool.Exec(t.Context(), "INSERT INTO platform_admins(user_id) VALUES($1)", actor); err != nil {
		t.Fatal(err)
	}
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Sender owner','sender-owner','active') RETURNING id", brand)
	other := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Sender other','sender-other','active') RETURNING id", brand)
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,provisioning_state) VALUES($1,'sender.test','active','fixture',now(),'ready') RETURNING id", tenant)
	scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,'self','self@sender.test','fixture',1024) RETURNING id", tenant, domain)
	alias := scalar("INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,kind) VALUES($1,$2,'manual','self@sender.test','alias') RETURNING id", tenant, domain)
	return maintenanceFixture{pool, Intent{TenantID: tenant, ActorID: actor, AliasID: alias, Reason: "Explicitly reviewed alias sender permission maintenance", SenderLogins: []string{"self@sender.test"}}, other}
}
func (f maintenanceFixture) grants(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(t.Context(), "SELECT cardinality(sender_logins) FROM mailbox_aliases WHERE tenant_id=$1 AND id=$2", f.intent.TenantID, f.intent.AliasID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMaintenanceGrantRevokeAndRedactedAudit(t *testing.T) {
	f := newMaintenanceFixture(t)
	ctx := t.Context()
	review, err := Preview(ctx, f.pool, f.intent)
	if err != nil {
		t.Fatal(err)
	}
	if f.grants(t) != 0 {
		t.Fatal("preview changed grants")
	}
	result, err := Apply(ctx, f.pool, f.intent, review.Digest)
	if err != nil || result.Code != "ALIAS_SENDERS_APPLIED" || f.grants(t) != 1 {
		t.Fatal("grant failed", err)
	}
	if _, err = Apply(ctx, f.pool, f.intent, review.Digest); !errors.Is(err, ErrStale) {
		t.Fatal("old review replay accepted", err)
	}
	revoke := f.intent
	revoke.SenderLogins = []string{}
	review, err = Preview(ctx, f.pool, revoke)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Apply(ctx, f.pool, revoke, review.Digest); err != nil || f.grants(t) != 0 {
		t.Fatal("revoke failed", err)
	}
	var count int
	var detail string
	if err = f.pool.QueryRow(ctx, "SELECT count(*),jsonb_agg(detail)::text FROM audit_logs WHERE tenant_id=$1 AND action='UPDATE alias.sender_logins'", f.intent.TenantID).Scan(&count, &detail); err != nil || count != 2 || strings.Contains(detail, "self@sender.test") {
		t.Fatal("audit missing or exposed private route", err)
	}
}

func TestMaintenanceRejectsAuthorityMailboxAndStaleState(t *testing.T) {
	f := newMaintenanceFixture(t)
	ctx := t.Context()
	review, err := Preview(ctx, f.pool, f.intent)
	if err != nil {
		t.Fatal(err)
	}
	wrong := f.intent
	wrong.TenantID = f.other
	if _, err = Preview(ctx, f.pool, wrong); !errors.Is(err, ErrAuthority) {
		t.Fatal("cross-tenant target accepted", err)
	}
	var otherDomain int64
	if err = f.pool.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,provisioning_state) VALUES($1,'other-sender.test','active','fixture',now(),'ready') RETURNING id", f.other).Scan(&otherDomain); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,'other','other@other-sender.test','fixture',1024)", f.other, otherDomain); err != nil {
		t.Fatal(err)
	}
	cross := f.intent
	cross.SenderLogins = []string{"other@other-sender.test"}
	if _, err = Preview(ctx, f.pool, cross); !errors.Is(err, ErrTarget) {
		t.Fatal("unknown mailbox accepted", err)
	}
	if _, err = f.pool.Exec(ctx, "UPDATE mailboxes SET send_enabled=false WHERE tenant_id=$1", f.intent.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = Apply(ctx, f.pool, f.intent, review.Digest); !errors.Is(err, ErrTarget) {
		t.Fatal("disabled sender accepted", err)
	}
	if _, err = f.pool.Exec(ctx, "UPDATE mailboxes SET send_enabled=true WHERE tenant_id=$1", f.intent.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, "UPDATE mailbox_aliases SET target='changed@example.test' WHERE tenant_id=$1 AND id=$2", f.intent.TenantID, f.intent.AliasID); err != nil {
		t.Fatal(err)
	}
	if _, err = Apply(ctx, f.pool, f.intent, review.Digest); !errors.Is(err, ErrStale) {
		t.Fatal("changed route accepted", err)
	}
	review, err = Preview(ctx, f.pool, f.intent)
	if err != nil {
		t.Fatal(err)
	}
	changed := f.intent
	changed.Reason = "A different explicit operator justification for this grant"
	if _, err = Apply(ctx, f.pool, changed, review.Digest); !errors.Is(err, ErrStale) {
		t.Fatal("changed reason accepted", err)
	}
	if _, err = f.pool.Exec(ctx, "DELETE FROM platform_admins WHERE user_id=$1", f.intent.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err = Apply(ctx, f.pool, f.intent, review.Digest); !errors.Is(err, ErrAuthority) {
		t.Fatal("revoked operator accepted", err)
	}
	if f.grants(t) != 0 {
		t.Fatal("rejected request changed grant")
	}
	if _, err = Preview(ctx, nil, f.intent); !errors.Is(err, ErrAuthority) {
		t.Fatal(err)
	}
}

func TestMaintenanceAuditFailureAndCommitUnknown(t *testing.T) {
	f := newMaintenanceFixture(t)
	ctx := t.Context()
	review, err := Preview(ctx, f.pool, f.intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `CREATE FUNCTION reject_sender_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='UPDATE alias.sender_logins' THEN RAISE EXCEPTION 'PRIVATE_FIXTURE_DETAIL';END IF;RETURN NEW;END $$; CREATE TRIGGER reject_sender_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_sender_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err = Apply(ctx, f.pool, f.intent, review.Digest); !errors.Is(err, ErrRejected) || strings.Contains(err.Error(), "PRIVATE_FIXTURE_DETAIL") {
		t.Fatal("audit failure not controlled", err)
	}
	if f.grants(t) != 0 {
		t.Fatal("audit failure committed authorization")
	}
	if _, err = f.pool.Exec(ctx, "DROP TRIGGER reject_sender_audit ON audit_logs"); err != nil {
		t.Fatal(err)
	}
	result, err := apply(ctx, f.pool, f.intent, review.Digest, func(tx pgx.Tx) error {
		if e := tx.Commit(ctx); e != nil {
			return e
		}
		return context.DeadlineExceeded
	})
	if !errors.Is(err, ErrUnknown) || result.Code != "ALIAS_SENDERS_COMMIT_UNKNOWN_DO_NOT_REPLAY" || f.grants(t) != 1 {
		t.Fatal("lost acknowledgement was claimed rolled back", err)
	}
	if _, err = Apply(ctx, f.pool, f.intent, review.Digest); !errors.Is(err, ErrStale) {
		t.Fatal("unknown committed operation replay accepted", err)
	}
}

func TestMaintenanceExplicitInputAndCatchall(t *testing.T) {
	f := newMaintenanceFixture(t)
	app, err := db.OpenApp(t.Context(), f.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if _, err = Preview(t.Context(), app, f.intent); !errors.Is(err, ErrAuthority) {
		t.Fatal("app role accepted privileged maintenance", err)
	}
	for _, addresses := range [][]string{nil, {"Self@sender.test"}, {"self@sender.test", "self@sender.test"}, {"self@sender.test\n"}, {"*@sender.test"}} {
		input := f.intent
		input.SenderLogins = addresses
		if _, err := Preview(t.Context(), f.pool, input); !errors.Is(err, ErrInput) {
			t.Fatal("invalid address list accepted", err)
		}
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE mailbox_aliases SET kind='catchall',local_part='' WHERE tenant_id=$1 AND id=$2", f.intent.TenantID, f.intent.AliasID); err != nil {
		t.Fatal(err)
	}
	if _, err := Preview(t.Context(), f.pool, f.intent); !errors.Is(err, ErrTarget) {
		t.Fatal("catchall sender granted", err)
	}
	revoke := f.intent
	revoke.SenderLogins = []string{}
	if _, err := Preview(t.Context(), f.pool, revoke); err != nil {
		t.Fatal("catchall revoke refused", err)
	}
}
