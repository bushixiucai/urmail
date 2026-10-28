package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	recall "github.com/bushixiucai/urmail/internal/domain/recall"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

type recallReviewFixture struct {
	platform, app                        *db.Pool
	manager                              *ApplyManager
	tenant, actor, other, entry, mailbox int64
	address, messageID, guid             string
	validity                             uint32
	present                              map[uint32]bool
	fail                                 map[uint32]bool
	mutations                            []uint32
}

func newRecallReviewFixture(t *testing.T) *recallReviewFixture {
	t.Helper()
	platform, app := testdb.OpenMigrated(t)
	f := &recallReviewFixture{platform: platform, app: app, address: "review@recall.test", messageID: "<review@recall.test>", guid: strings.Repeat("a", 32), validity: 77, present: map[uint32]bool{1: true, 2: true}, fail: map[uint32]bool{}}
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(t.Context(), query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name)VALUES('recall-review','Recall')RETURNING id")
	f.tenant = scalar("INSERT INTO tenants(brand_id,name,slug,status)VALUES($1,'Recall','recall-review','active')RETURNING id", brand)
	f.actor = scalar("INSERT INTO users(email,password_hash)VALUES('recall-owner@example.test','fixture-only')RETURNING id")
	f.other = scalar("INSERT INTO users(email,password_hash)VALUES('recall-other@example.test','fixture-only')RETURNING id")
	if _, err := platform.Exec(t.Context(), "INSERT INTO user_tenant_roles(tenant_id,user_id,role)VALUES($1,$2,'owner'),($1,$3,'admin')", f.tenant, f.actor, f.other); err != nil {
		t.Fatal(err)
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at)VALUES($1,'recall.test','active','fixture',now())RETURNING id", f.tenant)
	f.mailbox = scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status)VALUES($1,$2,'review',$3,'fixture-only','active')RETURNING id", f.tenant, domain, f.address)
	f.entry = scalar("INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,received_at,raw_sha256)VALUES($1,$2,$3,'fixture',now(),$4)RETURNING id", f.tenant, f.mailbox, f.messageID, strings.Repeat("e", 64))
	f.manager = &ApplyManager{pool: platform, output: f.output, run: func(_ context.Context, path string, args ...string) error {
		if path != "/usr/bin/doveadm" || len(args) != 9 || args[0] != "expunge" || args[2] != f.address || args[4] != f.guid || args[7] != "guid" {
			t.Fatalf("unsafe mutation command %s %v", path, args)
		}
		uid64, err := strconv.ParseUint(args[6], 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		uid := uint32(uid64)
		if args[8] != fmt.Sprintf("message-%d", uid) {
			t.Fatalf("missing immutable message GUID: %v", args)
		}
		f.mutations = append(f.mutations, uid)
		if f.fail[uid] {
			return fmt.Errorf("injected local expunge failure")
		}
		delete(f.present, uid)
		return nil
	}}
	return f
}
func (f *recallReviewFixture) output(_ context.Context, args ...string) ([]byte, error) {
	encode := json.Marshal
	for _, part := range args {
		if part == "status" {
			return encode([]map[string]any{{"mailbox": "INBOX", "guid": f.guid, "uidvalidity": strconv.FormatUint(uint64(f.validity), 10)}})
		}
	}
	if len(args) > 2 && args[2] == "search" {
		ids := make([]int, 0, len(f.present))
		for id := range f.present {
			ids = append(ids, int(id))
		}
		sort.Ints(ids)
		rows := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, map[string]any{"mailbox-guid": f.guid, "uid": id})
		}
		return encode(rows)
	}
	if len(args) > 2 && args[2] == "fetch" {
		id, _ := strconv.ParseUint(args[len(args)-1], 10, 32)
		if f.present[uint32(id)] {
			return encode([]map[string]any{{"hdr.message-id": f.messageID, "guid": fmt.Sprintf("message-%d", id)}})
		}
		return []byte("[]"), nil
	}
	return nil, fmt.Errorf("unexpected inventory command")
}
func (f *recallReviewFixture) review(t *testing.T, actor int64, selected []uint32) store.RecallRequests {
	t.Helper()
	params, _ := json.Marshal(RecallPreviewParams{TenantID: f.tenant, EntryID: f.entry, ActorID: actor})
	raw, _, err := f.manager.PreviewRecall(params)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot recall.Snapshot
	if err = json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	digest, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.app.UserTx(t.Context(), f.tenant, actor)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	q := store.New(tx)
	stored, err := q.StoreRecallReviewPreview(t.Context(), store.StoreRecallReviewPreviewParams{TenantID: f.tenant, ActorID: actor, EntryID: f.entry, Token: uuid.NewString(), Digest: digest, Snapshot: []byte(raw)})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(selected))
	for _, uid := range selected {
		for _, copy := range snapshot.Copies {
			if copy.UID == uid {
				keys = append(keys, copy.Key())
			}
		}
	}
	encoded, _ := json.Marshal(keys)
	confirmed, err := q.ConfirmRecallReview(t.Context(), store.ConfirmRecallReviewParams{TenantID: f.tenant, RecallID: stored.ID, ActorID: pgtype.Int8{Int64: actor, Valid: true}, Token: stored.PreviewToken, Digest: stored.PreviewDigest, Selected: encoded})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return store.RecallRequests(confirmed)
}
func (f *recallReviewFixture) execute(t *testing.T, row store.RecallRequests) store.RecallRequests {
	t.Helper()
	params, _ := json.Marshal(RecallParams{TenantID: f.tenant, RecallID: row.ID, AttemptEpoch: row.AttemptEpoch, PreviewToken: row.PreviewToken.String})
	if _, _, err := f.manager.ExpungeRecall(params); err != nil {
		t.Fatal(err)
	}
	current, err := store.New(f.platform.Pool).GetRecallByID(t.Context(), store.GetRecallByIDParams{TenantID: f.tenant, ID: row.ID})
	if err != nil {
		t.Fatal(err)
	}
	return current
}
func TestReviewedRecallPartialRetryUsesOnlyExplicitOriginalUIDsPG(t *testing.T) {
	f := newRecallReviewFixture(t)
	first := f.review(t, f.actor, []uint32{1, 2})
	f.fail[2] = true
	partial := f.execute(t, first)
	if partial.Status != "failed" || partial.Reason.String != "local_partial_result" || f.present[1] || !f.present[2] || len(f.mutations) != 2 {
		t.Fatalf("partial facts wrong: %s %s %v", partial.Status, partial.Reason.String, f.mutations)
	}
	f.present[3] = true
	f.fail[2] = false
	retry := f.review(t, f.other, []uint32{2})
	if retry.RequestedBy != f.actor || retry.PreviewActorID.Int64 != f.other || retry.AttemptEpoch != 2 {
		t.Fatal("new authorized review erased the historical actor")
	}
	done := f.execute(t, retry)
	if done.Status != "local_recalled" || f.present[2] || !f.present[3] || len(f.mutations) != 3 || f.mutations[2] != 2 {
		t.Fatal("retry expanded into an unselected/new copy")
	}
	f.present[4] = true
	again := f.execute(t, retry)
	if again.Status != "local_recalled" || len(f.mutations) != 3 || !f.present[3] || !f.present[4] {
		t.Fatal("completed intent replay mutated later mail")
	}
	old, _ := json.Marshal(RecallParams{TenantID: f.tenant, RecallID: first.ID, AttemptEpoch: first.AttemptEpoch, PreviewToken: first.PreviewToken.String})
	if _, _, err := f.manager.ExpungeRecall(old); err == nil {
		t.Fatal("old attempt nonce authorized a later review")
	}
}
func TestReviewedRecallRefusesHoldGenerationAndActorChangesPG(t *testing.T) {
	for _, kind := range []string{"hold", "generation", "new-copy", "role", "restored-intent"} {
		t.Run(kind, func(t *testing.T) {
			f := newRecallReviewFixture(t)
			row := f.review(t, f.actor, []uint32{1})
			switch kind {
			case "hold":
				if _, err := f.platform.Exec(t.Context(), "UPDATE archive_entries SET legal_hold=true WHERE tenant_id=$1 AND id=$2", f.tenant, f.entry); err != nil {
					t.Fatal(err)
				}
			case "generation":
				f.validity++
			case "new-copy":
				f.present[3] = true
			case "restored-intent":
				if _, err := f.platform.Exec(t.Context(), "UPDATE recall_requests SET status='uncertain', reason='RESTORE_RECONCILIATION_REQUIRED' WHERE tenant_id=$1 AND id=$2", f.tenant, row.ID); err != nil {
					t.Fatal(err)
				}
			case "role":
				if _, err := f.platform.Exec(t.Context(), "DELETE FROM user_tenant_roles WHERE tenant_id=$1 AND user_id=$2", f.tenant, f.actor); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "restored-intent" {
				params, _ := json.Marshal(RecallParams{TenantID: f.tenant, RecallID: row.ID, AttemptEpoch: row.AttemptEpoch, PreviewToken: row.PreviewToken.String})
				if _, _, err := f.manager.ExpungeRecall(params); err == nil || len(f.mutations) != 0 || !f.present[1] {
					t.Fatal("restored proof authorized a mutation")
				}
				return
			}
			current := f.execute(t, row)
			if current.Status == "local_recalled" || len(f.mutations) != 0 || !f.present[1] {
				t.Fatalf("%s bypassed final fence: %s", kind, current.Status)
			}
		})
	}
}
