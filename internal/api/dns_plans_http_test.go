package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/dnsprovider"
	domainmail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/onboarding"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
)

type localDNSTransport struct{ target *url.URL }

func (t localDNSTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copyRequest := r.Clone(r.Context())
	copyURL := *r.URL
	copyURL.Scheme = t.target.Scheme
	copyURL.Host = t.target.Host
	copyRequest.URL = &copyURL
	copyRequest.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(copyRequest)
}
func TestDNSPlansProductionHTTPWithProviderStandIn(t *testing.T) {
	app, platform := remediationHTTPDatabase(t)
	ctx := context.Background()
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('default','Default') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'DNS','dns','active') RETURNING id", brand)
	foreign := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Other','other','active') RETURNING id", brand)
	owner := scalar("INSERT INTO users(email,password_hash) VALUES('dns-owner@example.test','unused') RETURNING id")
	other := scalar("INSERT INTO users(email,password_hash) VALUES('dns-other@example.test','unused') RETURNING id")
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner'),($3,$4,'owner')", owner, tenant, other, foreign); err != nil {
		t.Fatal(err)
	}
	key, err := domainmail.GenerateDKIMKeypair("mail")
	if err != nil {
		t.Fatal(err)
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token,dkim_selector,dkim_private_key,provisioning_state,custom_web_enabled) VALUES($1,'dns.example.test','active',now(),'proof','mail',$2,'pending',false) RETURNING id", tenant, key.PrivateKey)
	q := store.New(platform.Pool)
	for _, item := range []struct {
		token        string
		user, tenant int64
	}{{"dns-owner", owner, tenant}, {"dns-other", other, foreign}} {
		verifiedUser, verifyErr := q.GetUserByID(ctx, item.user)
		if verifyErr != nil {
			t.Fatal(verifyErr)
		}
		if _, err = q.CreateSession(ctx, store.CreateSessionParams{ExpectedHash: verifiedUser.PasswordHash, ExpectedVersion: verifiedUser.CredentialVersion, ID: middleware.HashSessionToken(item.token), UserID: item.user, TenantID: pgtype.Int8{Int64: item.tenant, Valid: true}, CsrfToken: "csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	records := []map[string]any{{"id": "website", "type": "A", "name": "dns.example.test", "content": "192.0.2.55", "ttl": float64(300)}}
	writes := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		lockTx, lockErr := platform.PlatformTx(r.Context())
		if lockErr != nil {
			t.Error(lockErr)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var unlocked bool
		lockErr = lockTx.QueryRow(r.Context(), "SELECT pg_try_advisory_xact_lock($1)", tenant).Scan(&unlocked)
		_ = lockTx.Rollback(r.Context())
		if lockErr != nil || !unlocked {
			t.Errorf("provider network call held tenant business lock: %v %v", unlocked, lockErr)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer fixture-dns-secret" {
			t.Error("provider credential missing")
		}
		if r.URL.Path == "/client/v4/zones" {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{map[string]any{"id": "zone", "name": "example.test"}}})
			return
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": records, "result_info": map[string]any{"total_count": len(records)}})
			return
		}
		writes++
		var payload map[string]any
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("invalid provider request")
		}
		payload["id"] = fmt.Sprintf("created-%d", writes)
		records = append(records, payload)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": payload})
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := &onboarding.Service{Pool: platform, Secrets: box, Hostname: "mail.platform.test", Factory: func(provider string, c dnsprovider.Credentials) (dnsprovider.RecordManager, error) {
		manager, e := dnsprovider.NewRecordManager(provider, c)
		if e != nil {
			return nil, e
		}
		return manager.WithTransport(localDNSTransport{target}), nil
	}}
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.New(api.Options{Logger: zap.NewNop(), AppPool: app, PlatformPool: platform, Audit: q, Messages: messages, DNSPlans: service}))
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
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		content, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, []byte("fixture-dns-secret")) || bytes.Contains(content, []byte("PRIVATE KEY")) || bytes.Contains(content, []byte("encrypted_secret")) {
			t.Fatal("HTTP exposed credential or DKIM key")
		}
		var out map[string]any
		_ = json.Unmarshal(content, &out)
		return response.StatusCode, out
	}
	writeCount := func() int { mu.Lock(); defer mu.Unlock(); return writes }
	base := fmt.Sprintf("/api/v1/domains/%d/dns", domain)
	if code, _ := call(http.MethodPut, base+"/credentials", "dns-other", map[string]any{"provider": "cloudflare", "zone": "example.test", "credentials": map[string]string{"token": "fixture-dns-secret"}}); code < 400 {
		t.Fatal("foreign owner configured domain")
	}
	if code, out := call(http.MethodPut, base+"/credentials", "dns-owner", map[string]any{"provider": "cloudflare", "zone": "example.test", "credentials": map[string]string{"token": "fixture-dns-secret"}}); code != http.StatusNoContent {
		t.Fatalf("save %d %v", code, out)
	}
	if code, _ := call(http.MethodGet, base+"/credentials", "dns-owner", nil); code != http.StatusOK {
		t.Fatal(code)
	}
	code, draft := call(http.MethodPost, base+"/plans", "dns-owner", map[string]string{"kind": "provider"})
	if code != http.StatusCreated {
		t.Fatalf("preview %d %v", code, draft)
	}
	if writeCount() != 0 {
		t.Fatal("read-only preview wrote DNS")
	}
	id := draft["id"].(string)
	digest := draft["source_digest"].(string)
	if code, _ = call(http.MethodPost, base+"/plans/"+id+"/confirm", "dns-owner", map[string]any{"confirm": false, "source_digest": digest}); code < 400 {
		t.Fatal("implicit confirmation accepted")
	}
	if code, out := call(http.MethodPost, base+"/plans/"+id+"/confirm", "dns-owner", map[string]any{"confirm": true, "source_digest": digest}); code != http.StatusAccepted || out["status"] != "accepted" {
		t.Fatalf("confirm %d %v", code, out)
	}
	if worked, err := service.DNSOnce(ctx, "http-fixture-worker"); !worked || err != nil {
		t.Fatalf("worker %v %v", worked, err)
	}
	if writeCount() != 6 {
		t.Fatalf("writes=%d", writeCount())
	}
	if code, out := call(http.MethodGet, base+"/plans", "dns-owner", nil); code != http.StatusOK || !strings.Contains(fmt.Sprint(out), "completed") {
		t.Fatalf("plans %d %v", code, out)
	}
	var audited bool
	if err = platform.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM audit_logs WHERE action LIKE 'dns.record_%')").Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if !audited {
		t.Fatal("external DNS changes lack durable audit")
	}
	t.Logf("real production router + app/platform roles + loopback HTTP provider: preview writes=0, confirmed writes=%d; no external DNS or ACME", writeCount())
}
