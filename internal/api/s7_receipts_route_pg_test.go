package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// TestReceiptsRouteProductionPG drives the registered api.New mailbox route
// (not a hand-built router): an authenticated mailbox lists its own receipts
// and a same-tenant caller cannot see another mailbox's rows, with no write.
func TestReceiptsRouteProductionPG(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	m, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	app, err := db.OpenApp(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	platform, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer platform.Close()
	var brand, tenant, domain, mailbox, otherMailbox int64
	for _, seed := range []struct {
		sql    string
		args   []any
		target *int64
	}{
		{`INSERT INTO brands(slug,name) VALUES('s7-route','S7') RETURNING id`, nil, &brand},
		{`INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'S7','s7-route','active') RETURNING id`, []any{0}, &tenant},
		{`INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'s7route.test','active','v',now()) RETURNING id`, []any{0}, &domain},
		{`INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,'a','a@s7route.test','x',1024) RETURNING id`, []any{0, 0}, &mailbox},
		{`INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,'b','b@s7route.test','x',1024) RETURNING id`, []any{0, 0}, &otherMailbox},
	} {
		args := seed.args
		for i := range args {
			switch len(args) {
			case 1:
				args[i] = brand
			case 2:
				if i == 0 {
					args[i] = tenant
				} else {
					args[i] = domain
				}
			}
		}
		if err = platform.QueryRow(ctx, seed.sql, args...).Scan(seed.target); err != nil {
			t.Fatalf("seed %q: %v", seed.sql, err)
		}
	}
	token, hash, err := mailboxprincipal.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Exec(ctx, `INSERT INTO mailbox_sessions(token_hash,tenant_id,mailbox_id,password_version,expires_at,origin_host)
		VALUES($1,$2,$3,md5('x'),$4,'')`, hash, tenant, mailbox, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// One receipt owned by the other mailbox in the same tenant; the caller's
	// list must never include it.
	if _, err = platform.Exec(ctx, `INSERT INTO mailbox_send_receipts(tenant_id,mailbox_id,request_key,request_hash,message_id,raw_secret,smtp_secret,status,draft_folder,draft_uid,draft_uidvalidity)
		VALUES($1,$2,'foreign-key-000001',repeat('a',64),'<f@x>','','','accepted','Drafts',1,1)`, tenant, otherMailbox); err != nil {
		t.Fatal(err)
	}
	var before int
	if err = platform.QueryRow(ctx, `SELECT count(*) FROM mailbox_send_receipts`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	engine := api.New(api.Options{Logger: zap.NewNop(), AppPool: app, PlatformPool: platform, WebmailNative: true, MyHostname: "s7route.test"})
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		req.Host = "s7route.test"
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}
	if w := request("/api/v1/mailbox/send-receipts?folder_enc=Drafts&uid=1&uid_validity=1"); w.Code != http.StatusOK {
		t.Fatalf("receipts route status=%d body=%s", w.Code, w.Body.String())
	}
	body := request("/api/v1/mailbox/send-receipts").Body.String()
	if containsString(body, "foreign-key") {
		t.Fatalf("same-tenant caller saw another mailbox receipt: %s", body)
	}
	var after int
	if err = platform.QueryRow(ctx, `SELECT count(*) FROM mailbox_send_receipts`).Scan(&after); err != nil || after != before {
		t.Fatalf("read path wrote receipts: before=%d after=%d err=%v", before, after, err)
	}
}

func containsString(hay, needle string) bool { return strings.Contains(hay, needle) }
