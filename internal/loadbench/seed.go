package loadbench

import (
	"context"
	"fmt"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// Scale controls how much seed data a fixture carries. The numbers are part
// of the documented reference environment in CORE004_BUDGETS.md.
type Scale struct {
	Contacts            int
	RecurringEvents     int
	MarketingRecipients int
	Notifications       int
}

// DefaultScale is the reference data volume.
func DefaultScale() Scale {
	return Scale{Contacts: 500, RecurringEvents: 50, MarketingRecipients: 4000, Notifications: 2000}
}

// SmokeScale is the tiny data volume for -short runs.
func SmokeScale() Scale {
	return Scale{Contacts: 20, RecurringEvents: 5, MarketingRecipients: 200, Notifications: 200}
}

// Fixture holds the seeded identifiers the scenario operations need.
type Fixture struct {
	BrandID     int64
	TenantID    int64
	DomainID    int64
	MailboxID   int64
	Addressbook int64
	CalendarID  int64
	CampaignID  int64
}

// Seed populates a disposable fixture using the platform role. It uses raw
// INSERTs (not application paths) so the measured operations stay isolated.
func Seed(ctx context.Context, pool *db.Pool, scale Scale) (*Fixture, error) {
	f := &Fixture{}
	q := pool.Pool
	err := q.QueryRow(ctx, `INSERT INTO brands(slug,name) VALUES('loadbench','LoadBench') RETURNING id`).Scan(&f.BrandID)
	if err != nil {
		return nil, fmt.Errorf("seed brand: %w", err)
	}
	err = q.QueryRow(ctx, `INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'LB','loadbench-lb','active') RETURNING id`, f.BrandID).Scan(&f.TenantID)
	if err != nil {
		return nil, fmt.Errorf("seed tenant: %w", err)
	}
	err = q.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'loadbench.test','active','tok',now()) RETURNING id`, f.TenantID).Scan(&f.DomainID)
	if err != nil {
		return nil, fmt.Errorf("seed domain: %w", err)
	}
	err = q.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status,send_enabled) VALUES($1,$2,'ops','ops@loadbench.test','x','active',true) RETURNING id`, f.TenantID, f.DomainID).Scan(&f.MailboxID)
	if err != nil {
		return nil, fmt.Errorf("seed mailbox: %w", err)
	}
	err = q.QueryRow(ctx, `INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Contacts') RETURNING id`, f.TenantID, f.MailboxID).Scan(&f.Addressbook)
	if err != nil {
		return nil, fmt.Errorf("seed addressbook: %w", err)
	}
	if _, err = q.Exec(ctx, `INSERT INTO mailbox_contacts(tenant_id,mailbox_id,addressbook_id,display_name,emails)
		SELECT $1,$2,$3,'Seed Contact '||g, ('[{"value":"c'||g||'@loadbench.test","type":"HOME"}]')::jsonb
		FROM generate_series(1,$4) g`, f.TenantID, f.MailboxID, f.Addressbook, scale.Contacts); err != nil {
		return nil, fmt.Errorf("seed contacts: %w", err)
	}
	err = q.QueryRow(ctx, `INSERT INTO mailbox_calendars(tenant_id,mailbox_id,name,timezone) VALUES($1,$2,'Bench','Asia/Shanghai') RETURNING id`, f.TenantID, f.MailboxID).Scan(&f.CalendarID)
	if err != nil {
		return nil, fmt.Errorf("seed calendar: %w", err)
	}
	// Weekly recurring events inside the expansion window.
	if _, err = q.Exec(ctx, `INSERT INTO mailbox_events(tenant_id,mailbox_id,calendar_id,uid,summary,starts_at,ends_at,timezone,rrule,status)
		SELECT $1,$2,$3,'loadbench-'||g||'@loadbench.test','Recurring '||g,
			now()+make_interval(days=>g%7, hours=>g%10), now()+make_interval(days=>g%7, hours=>g%10)+interval '1 hour',
			'Asia/Shanghai','FREQ=WEEKLY;COUNT=10','confirmed'
		FROM generate_series(1,$4) g`, f.TenantID, f.MailboxID, f.CalendarID, scale.RecurringEvents); err != nil {
		return nil, fmt.Errorf("seed events: %w", err)
	}
	// Marketing campaign with an enabled dedicated_gateway channel and
	// opted-in recipients, so the full claim/authorize/finish step is legal.
	var channelID int64
	err = q.QueryRow(ctx, `INSERT INTO marketing_channels(tenant_id,name,kind,sender_address,encrypted_config,enabled)
		VALUES($1,'bench','dedicated_gateway','ops@loadbench.test','enc:v1:bench',true) RETURNING id`, f.TenantID).Scan(&channelID)
	if err != nil {
		return nil, fmt.Errorf("seed channel: %w", err)
	}
	var templateID, listID int64
	err = q.QueryRow(ctx, `INSERT INTO marketing_templates(tenant_id,name,subject,text_body) VALUES($1,'bench','Bench','Body') RETURNING id`, f.TenantID).Scan(&templateID)
	if err != nil {
		return nil, fmt.Errorf("seed template: %w", err)
	}
	err = q.QueryRow(ctx, `INSERT INTO marketing_lists(tenant_id,name) VALUES($1,'bench') RETURNING id`, f.TenantID).Scan(&listID)
	if err != nil {
		return nil, fmt.Errorf("seed list: %w", err)
	}
	if _, err = q.Exec(ctx, `INSERT INTO marketing_contacts(tenant_id,list_id,email,consent_source)
		SELECT $1,$2,'r'||g||'@loadbench.test','bench-import' FROM generate_series(1,$3) g
		ON CONFLICT DO NOTHING`, f.TenantID, listID, scale.MarketingRecipients); err != nil {
		return nil, fmt.Errorf("seed marketing contacts: %w", err)
	}
	err = q.QueryRow(ctx, `INSERT INTO marketing_campaigns(tenant_id,name,channel_id,template_id,list_id,status,subject_snapshot,body_snapshot,scheduled_at)
		VALUES($1,'bench',$2,$3,$4,'scheduled','Bench','Body',now()-interval '1 minute') RETURNING id`,
		f.TenantID, channelID, templateID, listID).Scan(&f.CampaignID)
	if err != nil {
		return nil, fmt.Errorf("seed campaign: %w", err)
	}
	if _, err = q.Exec(ctx, `INSERT INTO marketing_recipients(tenant_id,campaign_id,email)
		SELECT $1,$2,'r'||g||'@loadbench.test' FROM generate_series(1,$3) g`, f.TenantID, f.CampaignID, scale.MarketingRecipients); err != nil {
		return nil, fmt.Errorf("seed recipients: %w", err)
	}
	if _, err = q.Exec(ctx, `INSERT INTO notification_outbox(tenant_id,kind,message_key,params,dedupe_key,channel,state,recipient,recipient_verified)
		SELECT $1,'bench','notification.bench','{}'::jsonb,'loadbench:'||g,'email','pending','ops@loadbench.test',true
		FROM generate_series(1,$2) g`, f.TenantID, scale.Notifications); err != nil {
		return nil, fmt.Errorf("seed notifications: %w", err)
	}
	return f, nil
}

// queries builds a store handle over the pool for scenario operations.
func queries(pool *db.Pool) *store.Queries { return store.New(pool.Pool) }
