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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/store"
)

type recallExpungerFunc func(context.Context, int64, int64) error

func (f recallExpungerFunc) ExpungeRecall(ctx context.Context, tenantID, recallID int64) error {
	return f(ctx, tenantID, recallID)
}

// The production router uses real app/platform roles and actual HTTP. The typed
// agent stand-in completes only committed requests; agent/doveadm tests own the
// privileged process and exact-message deletion boundary.
func TestRecallHTTPWithRealRoles(t *testing.T) {
	app, platform := remediationHTTPDatabase(t)
	ctx := context.Background()
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := platform.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := platform.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('default','Default') RETURNING id")
	owner := scalar("INSERT INTO users(email,password_hash) VALUES('recall-owner@example.test','placeholder') RETURNING id")
	other := scalar("INSERT INTO users(email,password_hash) VALUES('recall-other@example.test','placeholder') RETURNING id")
	member := scalar("INSERT INTO users(email,password_hash) VALUES('recall-member@example.test','placeholder') RETURNING id")
	a := scalar("INSERT INTO tenants(brand_id,name,slug) VALUES($1,'A','a') RETURNING id", brand)
	b := scalar("INSERT INTO tenants(brand_id,name,slug) VALUES($1,'B','b') RETURNING id", brand)
	exec("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner'),($3,$4,'owner'),($5,$2,'member')", owner, a, other, b, member)
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,verification_token,status,verified_at,provisioning_state) VALUES($1,'recall.example.test','fixture-token','active',now(),'ready') RETURNING id", a)
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,'one','one@recall.example.test','placeholder',1) RETURNING id", a, domain)
	archive := func(message string, box any, held bool) int64 {
		return scalar("INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,received_at,legal_hold) VALUES($1,$2,$3,'/archive/fixture',now(),$4) RETURNING id", a, box, message, held)
	}
	local := archive("<local@example.test>", mailbox, false)
	held := archive("<held@example.test>", mailbox, true)
	remote := archive("<remote@example.test>", nil, false)
	retry := archive("<retry@example.test>", mailbox, false)
	q := store.New(platform.Pool)
	for _, v := range []struct {
		token    string
		uid, tid int64
	}{{"recall-owner", owner, a}, {"recall-other", other, b}, {"recall-member", member, a}} {
		verifiedUser, verifyErr := q.GetUserByID(ctx, v.uid)
		if verifyErr != nil {
			t.Fatal(verifyErr)
		}
		if _, err := q.CreateSession(ctx, store.CreateSessionParams{ExpectedHash: verifiedUser.PasswordHash, ExpectedVersion: verifiedUser.CredentialVersion, ID: middleware.HashSessionToken(v.token), UserID: v.uid, TenantID: pgtype.Int8{Int64: v.tid, Valid: true}, CsrfToken: "csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	var fail atomic.Bool
	expunger := recallExpungerFunc(func(ctx context.Context, tid, rid int64) error {
		calls.Add(1)
		// This separate platform connection cannot see an uncommitted request.
		row, err := q.GetRecallByID(ctx, store.GetRecallByIDParams{TenantID: tid, ID: rid})
		if err != nil {
			return err
		}
		if row.Status != "pending" {
			return nil
		}
		if fail.Load() {
			return errors.New("synthetic transport unavailable")
		}
		status, reason := "local_recalled", ""
		if !row.MailboxID.Valid {
			status, reason = "failed", "remote_delivery_not_recallable"
		}
		_, err = q.PlatformCompleteRecall(ctx, store.PlatformCompleteRecallParams{TenantID: tid, ID: rid, Status: status, Reason: pgtype.Text{String: reason, Valid: reason != ""}})
		return err
	})
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.New(api.Options{Logger: zap.NewNop(), AppPool: app, PlatformPool: platform, Messages: messages, Recall: expunger, Audit: q}))
	defer server.Close()
	call := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		r, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-CSRF-Token", "csrf")
		r.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		payload, _ := io.ReadAll(res.Body)
		out := map[string]any{}
		if err = json.Unmarshal(payload, &out); err != nil {
			t.Fatalf("invalid JSON %s", payload)
		}
		return res.StatusCode, out
	}
	post := func(id int64, token string) (int, map[string]any) {
		return call(http.MethodPost, "/api/v1/compliance/recalls", token, map[string]int64{"archive_entry_id": id})
	}
	for _, v := range []struct {
		id    int64
		token string
		want  int
	}{{held, "recall-owner", 409}, {local, "recall-other", 404}, {local, "recall-member", 403}} {
		status, out := post(v.id, v.token)
		if status != v.want {
			t.Fatalf("denied recall %d: %d %#v", v.id, status, out)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("denied request reached root agent")
	}
	status, out := post(local, "recall-owner")
	if status != 200 {
		t.Fatalf("local recall: %d %#v", status, out)
	}
	row := out["recall"].(map[string]any)
	id := int64(row["id"].(float64))
	if row["status"] != "local_recalled" || row["scope"] != "local_mailbox" {
		t.Fatal(row)
	}
	status, out = post(local, "recall-owner")
	if status != 200 || int64(out["recall"].(map[string]any)["id"].(float64)) != id || calls.Load() != 1 {
		t.Fatal("duplicate completed recall reached agent")
	}
	if scalar("SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='compliance.recall.complete'", a) != 1 {
		t.Fatal("completion audit not durable exactly once")
	}
	if scalar("SELECT count(*) FROM archive_entries WHERE id=$1 AND deleted_at IS NULL", local) != 1 {
		t.Fatal("recall destroyed compliance archive")
	}
	if status, _ = call(http.MethodGet, fmt.Sprintf("/api/v1/compliance/recalls/%d", id), "recall-other", nil); status != 404 {
		t.Fatal("cross-tenant recall disclosure")
	}
	if status, out = post(remote, "recall-owner"); status != 200 || out["recall"].(map[string]any)["status"] != "failed" || out["recall"].(map[string]any)["reason"] != "remote_delivery_not_recallable" {
		t.Fatal("remote recall claimed success", status, out)
	}
	fail.Store(true)
	status, out = post(retry, "recall-owner")
	if status != 202 || out["recall"].(map[string]any)["status"] != "pending" {
		t.Fatal("transport failure lost pending request", status, out)
	}
	pendingID := out["recall"].(map[string]any)["id"]
	fail.Store(false)
	status, out = post(retry, "recall-owner")
	if status != 200 || out["recall"].(map[string]any)["id"] != pendingID || out["recall"].(map[string]any)["status"] != "local_recalled" {
		t.Fatal("pending request did not resume", status, out)
	}
	if status, out = call(http.MethodGet, "/api/v1/compliance/recalls", "recall-owner", nil); status != 200 || len(out["recalls"].([]any)) != 3 {
		t.Fatal("recall list failed", status, out)
	}
}
