package testdb

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestEventUpdateKeepsAttendeeOnSameRow(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, domain, mailbox, calendar int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('att','Att') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'A','att-a','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'att.test','active','tok',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@att.test','x','active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_calendars(tenant_id,mailbox_id,name) VALUES($1,$2,'Calendar') RETURNING id", tenant, mailbox).Scan(&calendar); err != nil {
		t.Fatal(err)
	}
	start := pgtype.Timestamptz{Time: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), Valid: true}
	end := pgtype.Timestamptz{Time: start.Time.Add(time.Hour), Valid: true}
	q := store.New(tx)
	row, err := q.PlatformInsertEvent(ctx, store.PlatformInsertEventParams{TenantID: tenant, MailboxID: mailbox, CalendarID: calendar, Uid: "uid-1", Summary: "A", StartsAt: start, EndsAt: end, Timezone: "UTC", Status: "confirmed", Attendees: []byte("[]")})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := q.PlatformUpdateEvent(ctx, store.PlatformUpdateEventParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID, ExpectedVersion: row.Version, Summary: "A", StartsAt: start, EndsAt: end, Timezone: "UTC", Status: "confirmed", Attendees: []byte(`["ada@example.test"]`)})
	if err != nil || updated.ID != row.ID || string(updated.Attendees) != `["ada@example.test"]` {
		t.Fatalf("%+v %v", updated, err)
	}
}
