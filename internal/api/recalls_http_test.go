package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	domainmail "github.com/bushixiucai/urmail/internal/domain/mail"
	recall "github.com/bushixiucai/urmail/internal/domain/recall"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/store"
)

type reviewedHTTPAgent struct {
	pool  *db.Pool
	calls atomic.Int32
	fail  atomic.Bool
}

func (a *reviewedHTTPAgent) ExpungeRecall(context.Context, int64, int64) error {
	return errors.New("unreviewed operation disabled")
}
func (a *reviewedHTTPAgent) PreviewRecall(ctx context.Context, tenant, entry, actor int64) (recall.Snapshot, error) {
	tx, err := a.pool.PlatformTx(ctx)
	if err != nil {
		return recall.Snapshot{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if err = q.PlatformLockRecallTenant(ctx, tenant); err != nil {
		return recall.Snapshot{}, err
	}
	source, err := q.PlatformRecallPreviewSource(ctx, store.PlatformRecallPreviewSourceParams{TenantID: tenant, EntryID: entry, ActorID: actor})
	if err != nil {
		return recall.Snapshot{}, err
	}
	snapshot := recall.Snapshot{Version: 1, TenantID: tenant, ActorID: actor, EntryID: entry, MailboxID: source.MailboxID.Int64, Address: source.Address.String, MessageID: source.MessageID, OriginalSHA256: source.OriginalSha256, Held: source.LegalHold, HoldConflict: source.HoldConflict, Deleted: source.DeletedAt.Valid, TenantStatus: source.TenantStatus, MailboxStatus: source.MailboxStatus.String, DomainStatus: source.DomainStatus.String}
	if err = json.Unmarshal(source.Sources, &snapshot.Sources); err != nil {
		return recall.Snapshot{}, err
	}
	if !snapshot.Held && !snapshot.HoldConflict {
		snapshot.Copies = []recall.Copy{{GUID: strings.Repeat("a", 32), MessageGUID: fmt.Sprintf("synthetic-message-%d", entry), Folder: "INBOX", UIDValidity: 77, UID: 1}}
	}
	return snapshot, nil
}
func (a *reviewedHTTPAgent) ExpungeReviewedRecall(ctx context.Context, tenant, id, epoch int64, token string) error {
	if a.fail.Load() {
		return errors.New("synthetic agent transport unavailable")
	}
	tx, err := a.pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if err = q.PlatformLockRecallTenant(ctx, tenant); err != nil {
		return err
	}
	row, err := q.PlatformLockRecallForExpunge(ctx, store.PlatformLockRecallForExpungeParams{TenantID: tenant, ID: id})
	if err != nil {
		return err
	}
	if row.AttemptEpoch != epoch || row.PreviewToken.String != token {
		return errors.New("review mismatch")
	}
	if row.Status != "pending" {
		return nil
	}
	a.calls.Add(1)
	var keys []string
	if err = json.Unmarshal(row.SelectedCopyKeys, &keys); err != nil {
		return err
	}
	result := recall.Result{Items: []recall.ItemResult{}}
	for _, key := range keys {
		result.Items = append(result.Items, recall.ItemResult{Key: key, Status: "confirmed_absent", Reason: "local_copy_absent"})
	}
	raw, _ := json.Marshal(result)
	_, err = q.PlatformFinishReviewedRecall(ctx, store.PlatformFinishReviewedRecallParams{TenantID: tenant, RecallID: id, Epoch: epoch, Status: "local_recalled", Reason: "local_snapshot_absent", Result: raw})
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// The production route tree uses real authentication, CSRF, RLS and PostgreSQL.
// The named agent adapter locks the same tenant fence in another connection,
// proving that neither preview nor confirmation holds it across the RPC.
func TestRecallHTTPWithRealRoles(t *testing.T) {
	app, platform := remediationHTTPDatabase(t)
	ctx := t.Context()
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := platform.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	password := "Synthetic-recall-browser-only!42"
	hash, err := domainmail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	brand := scalar("INSERT INTO brands(slug,name)VALUES('default','Recall fixture')RETURNING id")
	owner := scalar("INSERT INTO users(email,password_hash)VALUES('recall-owner@example.test',$1)RETURNING id", hash)
	other := scalar("INSERT INTO users(email,password_hash)VALUES('recall-other@example.test','unused')RETURNING id")
	member := scalar("INSERT INTO users(email,password_hash)VALUES('recall-member@example.test','unused')RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status)VALUES($1,'Recall A','recall-a','active')RETURNING id", brand)
	foreign := scalar("INSERT INTO tenants(brand_id,name,slug,status)VALUES($1,'Recall B','recall-b','active')RETURNING id", brand)
	exec("INSERT INTO user_tenant_roles(user_id,tenant_id,role)VALUES($1,$2,'owner'),($3,$4,'owner'),($5,$2,'member')", owner, tenant, other, foreign, member)
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token,provisioning_state)VALUES($1,'recall.example.test','active',now(),'fixture','ready')RETURNING id", tenant)
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash)VALUES($1,$2,'one','one@recall.example.test','unused')RETURNING id", tenant, domain)
	archive := func(message string, box any, held bool) int64 {
		return scalar("INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,received_at,legal_hold,raw_sha256,subject)VALUES($1,$2,$3,'fixture',now(),$4,$5,'Synthetic recall source')RETURNING id", tenant, box, message, held, strings.Repeat("e", 64))
	}
	local := archive("<local@example.test>", mailbox, false)
	held := archive("<held@example.test>", mailbox, true)
	remote := archive("<remote@example.test>", nil, false)
	retry := archive("<retry@example.test>", mailbox, false)
	stale := archive("<stale@example.test>", mailbox, false)
	q := store.New(platform)
	for _, session := range []struct {
		token    string
		uid, tid int64
	}{{"recall-owner", owner, tenant}, {"recall-other", other, foreign}, {"recall-member", member, tenant}} {
		user, e := q.GetUserByID(ctx, session.uid)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = q.CreateSession(ctx, store.CreateSessionParams{ExpectedHash: user.PasswordHash, ExpectedVersion: user.CredentialVersion, ID: middleware.HashSessionToken(session.token), UserID: session.uid, TenantID: pgtype.Int8{Int64: session.tid, Valid: true}, CsrfToken: "csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); e != nil {
			t.Fatal(e)
		}
	}
	agent := &reviewedHTTPAgent{pool: platform}
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.New(api.Options{Logger: zap.NewNop(), AppPool: app, PlatformPool: platform, Messages: messages, Recall: agent, Audit: q}))
	defer server.Close()
	client := &http.Client{Timeout: 6 * time.Second}
	call := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		request, e := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-CSRF-Token", "csrf")
		request.Header.Set("Content-Type", "application/json")
		response, e := client.Do(request)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		payload, _ := io.ReadAll(response.Body)
		out := map[string]any{}
		if e = json.Unmarshal(payload, &out); e != nil {
			t.Fatalf("invalid JSON HTTP%d %s", response.StatusCode, payload)
		}
		return response.StatusCode, out
	}
	preview := func(entry int64, token string) (int, map[string]any) {
		return call("POST", "/api/v1/compliance/recalls/preview", token, map[string]any{"archive_entry_id": entry, "include_related": true})
	}
	confirmation := func(out map[string]any) map[string]any {
		target := out["targets"].([]any)[0].(map[string]any)
		row := target["recall"].(map[string]any)
		copies := target["copies"].([]any)
		keys := make([]string, 0, len(copies))
		for _, value := range copies {
			keys = append(keys, value.(map[string]any)["key"].(string))
		}
		return map[string]any{"id": row["id"], "preview_token": row["preview_token"], "digest": row["preview_digest"], "copy_keys": keys}
	}
	confirm := func(target map[string]any) (int, map[string]any) {
		return call("POST", "/api/v1/compliance/recalls", "recall-owner", map[string]any{"confirmed": true, "targets": []any{target}})
	}
	if status, _ := call("POST", "/api/v1/compliance/recalls", "recall-owner", map[string]any{"archive_entry_id": local}); status != 400 {
		t.Fatalf("legacy immediate path remains %d", status)
	}
	for _, test := range []struct {
		token string
		want  int
	}{{"recall-other", 404}, {"recall-member", 403}} {
		if status, out := preview(local, test.token); status != test.want {
			t.Fatalf("denied preview %d %v", status, out)
		}
	}
	for _, test := range []struct {
		id     int64
		reason string
	}{{held, "legal_hold"}, {remote, "remote_delivery_not_recallable"}} {
		status, out := preview(test.id, "recall-owner")
		if status != 200 || out["targets"].([]any)[0].(map[string]any)["reason"] != test.reason {
			t.Fatalf("held/remote projection %d %v", status, out)
		}
	}
	status, out := preview(local, "recall-owner")
	if status != 200 {
		t.Fatalf("local preview %d %v", status, out)
	}
	target := confirmation(out)
	status, out = confirm(target)
	if status != 200 || out["recalls"].([]any)[0].(map[string]any)["status"] != "local_recalled" {
		t.Fatalf("confirmation %d %v", status, out)
	}
	if status, out = confirm(target); status != 200 || agent.calls.Load() != 1 {
		t.Fatalf("duplicate confirmation reran mutation %d %v", status, out)
	}
	id := int64(target["id"].(float64))
	if status, _ = call("GET", fmt.Sprintf("/api/v1/compliance/recalls/%d", id), "recall-other", nil); status != 404 {
		t.Fatal("cross-tenant recall visible")
	}
	if scalar("SELECT count(*) FROM archive_entries WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL", tenant, local) != 1 {
		t.Fatal("local recall removed archive evidence")
	}
	status, out = preview(stale, "recall-owner")
	if status != 200 {
		t.Fatal(out)
	}
	staleTarget := confirmation(out)
	exec("UPDATE archive_entries SET legal_hold=true WHERE tenant_id=$1 AND id=$2", tenant, stale)
	if status, out = confirm(staleTarget); status != 409 || agent.calls.Load() != 1 {
		t.Fatalf("hold changed after preview %d %v", status, out)
	}
	status, out = preview(retry, "recall-owner")
	if status != 200 {
		t.Fatal(out)
	}
	retryTarget := confirmation(out)
	agent.fail.Store(true)
	if status, out = confirm(retryTarget); status != 202 || out["recalls"].([]any)[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("lost transport uncertainty %d %v", status, out)
	}
	agent.fail.Store(false)
	if status, out = confirm(retryTarget); status != 200 || agent.calls.Load() != 2 {
		t.Fatalf("same committed intent not recovered %d %v", status, out)
	}
	if scalar("SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='compliance.recall.review_complete'", tenant) != 2 {
		t.Fatal("completion audit missing/duplicated")
	}
	if directory := os.Getenv("URMAIL_RECALL_BROWSER_DIR"); directory != "" {
		if err = os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		fixture, _ := json.Marshal(map[string]any{"url": server.URL, "email": "recall-owner@example.test", "password": password, "local_entry_id": local, "held_entry_id": held, "remote_entry_id": remote, "adapter": "explicit database-fenced agent stand-in; real doveadm tested independently"})
		if err = os.WriteFile(filepath.Join(directory, "fixture.json"), fixture, 0600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(15 * time.Minute)
		for time.Now().Before(deadline) {
			if data, e := os.ReadFile(filepath.Join(directory, "browser.done")); e == nil {
				if strings.TrimSpace(string(data)) != "PASS" {
					t.Fatal("browser failed")
				}
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
		t.Fatal("recall browser timed out")
	}
}
