package db_test

import (
	"context"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestRestoreRewindHoldsNewSideEffectsAndInvalidatesDerivedState(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	ctx := context.Background()
	conn := platform.Pool
	_, err := conn.Exec(ctx, `DO $$ DECLARE b bigint;t bigint;d bigint;m bigint;f bigint; BEGIN
 INSERT INTO brands(slug,name) VALUES('rewind','Restore Rewind') RETURNING id INTO b;
 INSERT INTO tenants(brand_id,name,slug,status) VALUES(b,'Restore Rewind','rewind','active') RETURNING id INTO t;
 INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES(t,'rewind.test','active','proof',now()) RETURNING id INTO d;
 INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES(t,d,'a','a@rewind.test','x','active') RETURNING id INTO m;
 INSERT INTO mailbox_sync_cursors(tenant_id,mailbox_id,protocol,client_key,cursor_id,epoch) VALUES(t,m,'carddav','phone',70,3);
 INSERT INTO mailbox_eas_devices(tenant_id,mailbox_id,device_id,protocol_version,policy_key,provisioned,ping_heartbeat,ping_folders) VALUES(t,m,'phone','14.1',123,true,300,'["e:INBOX"]'::jsonb);
 INSERT INTO mailbox_eas_folder_state(tenant_id,mailbox_id,device_id,folder_id,display_name,folder_type,change_seq) VALUES(t,m,'phone','e:INBOX','Inbox',2,70);
 INSERT INTO mailbox_eas_collection_state(tenant_id,mailbox_id,device_id,folder_id,sync_epoch,change_seq,uid_validity) VALUES(t,m,'phone','e:INBOX',3,70,9);
 INSERT INTO mailbox_eas_item_state(tenant_id,mailbox_id,device_id,folder_id,item_id,fingerprint) VALUES(t,m,'phone','e:INBOX','item-1','old');
 INSERT INTO mailbox_ai_chunks(tenant_id,mailbox_id,folder,folder_uidvalidity,message_uid,chunk_index,content_version,content,content_digest) VALUES(t,m,'INBOX',9,1,0,1,'revoked after snapshot',repeat('a',64));
 INSERT INTO mailbox_jobs(tenant_id,mailbox_id,kind,idempotency_key,status) VALUES(t,m,'calendar_invite','invite-pending','pending'),(t,m,'calendar_invite','invite-done','done');
 INSERT INTO marketing_flows(tenant_id,name,status) VALUES(t,'restore flow','active') RETURNING id INTO f;
 INSERT INTO marketing_flow_steps(tenant_id,flow_id,step_key,kind,next_key,position) VALUES(t,f,'send','send','stop',0),(t,f,'stop','stop','',1);
 INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key,delivery_uncertain,lease_owner,lease_until,lease_fence) VALUES(t,f,'outside@example.test','send',false,'old-worker',now()+interval '1 hour',7);
 INSERT INTO subscription_renewal_attempts(tenant_id,subscription_id,period_end,state) VALUES(t,7001,now(),'pending'),(t,7002,now(),'succeeded');
 END $$`)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.New(conn).PlatformRestoreSyncEpoch(ctx)
	if err != nil || before != 1 {
		t.Fatalf("initial epoch=%d %v", before, err)
	}
	if err = db.HoldRestoredSideEffects(ctx, conn.Config().ConnConfig.ConnString(), "rewind-generation-1"); err != nil {
		t.Fatal(err)
	}
	epoch, err := store.New(app.Pool).PlatformRestoreSyncEpoch(ctx)
	if err != nil || epoch <= 3 {
		t.Fatalf("new epoch=%d %v", epoch, err)
	}
	checks := map[string]string{
		"renewal cannot re-charge":       `SELECT state='canceled' AND last_error LIKE '%RESTORE_RECONCILIATION_REQUIRED%' FROM subscription_renewal_attempts WHERE subscription_id=7001`,
		"successful payment preserved":   `SELECT state='succeeded' FROM subscription_renewal_attempts WHERE subscription_id=7002`,
		"flow paused":                    `SELECT status='paused' FROM marketing_flows WHERE name='restore flow'`,
		"flow fence invalidated":         `SELECT delivery_uncertain AND lease_owner IS NULL AND lease_until IS NULL AND lease_fence>7 FROM marketing_flow_positions`,
		"pending invitation held":        `SELECT status='uncertain' FROM mailbox_jobs WHERE idempotency_key='invite-pending'`,
		"completed invitation preserved": `SELECT status='done' FROM mailbox_jobs WHERE idempotency_key='invite-done'`,
		"old AI plaintext purged":        `SELECT NOT EXISTS(SELECT 1 FROM mailbox_ai_chunks)`,
		"old EAS snapshots purged":       `SELECT NOT EXISTS(SELECT 1 FROM mailbox_eas_item_state) AND NOT EXISTS(SELECT 1 FROM mailbox_eas_folder_state)`,
		"device reprovision required":    `SELECT NOT provisioned AND policy_key=0 AND ping_heartbeat=0 AND ping_folders='[]'::jsonb FROM mailbox_eas_devices`,
		"collection epoch reset":         `SELECT sync_epoch=urmail_restore_sync_epoch() AND change_seq=0 AND uid_validity=0 FROM mailbox_eas_collection_state`,
		"DAV cursor reset":               `SELECT epoch=urmail_restore_sync_epoch() AND cursor_id=0 FROM mailbox_sync_cursors`,
	}
	for name, query := range checks {
		var ok bool
		if err = conn.QueryRow(ctx, query).Scan(&ok); err != nil || !ok {
			t.Errorf("%s: %v %v", name, ok, err)
		}
	}
	// The normal lease/uncertainty maintenance must not reactivate a restored
	// flow or cause an outbound message after quarantine release.
	if _, err = conn.Exec(ctx, `UPDATE marketing_flow_positions SET updated_at=now()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	q := store.New(conn)
	if _, err = q.PlatformRecoverMarketingFlowUncertain(ctx); err != nil {
		t.Fatal(err)
	}
	claimed, err := q.PlatformClaimMarketingFlowPositions(ctx, store.PlatformClaimMarketingFlowPositionsParams{Batch: 10, LeaseOwner: "new-worker", LeaseSeconds: 60})
	if err != nil || len(claimed) != 0 {
		t.Fatalf("restored flow became sendable: %d %v", len(claimed), err)
	}
	if err = db.HoldRestoredSideEffects(ctx, conn.Config().ConnConfig.ConnString(), "rewind-generation-1"); err != nil {
		t.Fatal(err)
	}
	resumed, err := q.PlatformRestoreSyncEpoch(ctx)
	if err != nil || resumed != epoch {
		t.Fatalf("resumed restore changed epoch: %d %v", resumed, err)
	}
}
