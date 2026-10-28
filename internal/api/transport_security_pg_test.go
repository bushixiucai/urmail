package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/mailtransport"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

type transportFixture struct {
	platform, app  *db.Pool
	engine         *gin.Engine
	tenant, domain int64
	token, csrf    string
}

func newTransportFixture(t *testing.T) *transportFixture {
	t.Helper()
	platform, app := testdb.OpenMigrated(t)
	ctx := t.Context()
	hash, err := panelpassword.Hash("Synthetic-transport-owner!42")
	if err != nil {
		t.Fatal(err)
	}
	var brand, owner, tenant, domain int64
	for _, s := range []struct {
		query string
		args  []any
		dest  *int64
	}{
		{"INSERT INTO brands(slug,name) VALUES('default','Transport test') RETURNING id", nil, &brand},
		{"INSERT INTO users(email,password_hash) VALUES('transport-owner@example.test',$1) RETURNING id", []any{hash}, &owner},
	} {
		if err = platform.QueryRow(ctx, s.query, s.args...).Scan(s.dest); err != nil {
			t.Fatal(err)
		}
	}
	if err = platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Transport test','transport-test','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", owner, tenant); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token,provisioning_state) VALUES($1,'transport.example.test','active',now(),'fixture','ready') RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	f := &transportFixture{platform: platform, app: app, tenant: tenant, domain: domain}
	f.engine = New(Options{PlatformPool: platform, AppPool: app, Logger: zap.NewNop(), Messages: messages, MyHostname: "mail.platform.test", WorkersHeld: true})
	login := f.call(t, "POST", "/api/v1/auth/login", map[string]string{"email": "transport-owner@example.test", "password": "Synthetic-transport-owner!42"}, "", false)
	var session struct {
		Token string `json:"token"`
		CSRF  string `json:"csrf_token"`
	}
	if login.Code != 200 || json.Unmarshal(login.Body.Bytes(), &session) != nil || session.Token == "" {
		t.Fatalf("login %d %s", login.Code, login.Body)
	}
	f.token, f.csrf = session.Token, session.CSRF
	return f
}
func (f *transportFixture) call(t *testing.T, method, path string, body any, host string, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	request := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	if host != "" {
		request.Host = host
	}
	if f.token != "" {
		request.Header.Set("Authorization", "Bearer "+f.token)
	}
	if csrf {
		request.Header.Set("X-CSRF-Token", f.csrf)
	}
	response := httptest.NewRecorder()
	f.engine.ServeHTTP(response, request)
	return response
}
func (f *transportFixture) path() string {
	return fmt.Sprintf("/api/v1/domains/%d/transport-security", f.domain)
}
func decodeTransport(t *testing.T, w *httptest.ResponseRecorder, want int) map[string]any {
	t.Helper()
	if w.Code != want {
		t.Fatalf("HTTP%d want%d %s", w.Code, want, w.Body)
	}
	var out map[string]any
	if w.Body.Len() > 0 && json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("invalid JSON %s", w.Body)
	}
	return out
}
func TestTransportPolicyPublicationHostBindingAndCachePG(t *testing.T) {
	f := newTransportFixture(t)
	ctx := t.Context()
	initial := decodeTransport(t, f.call(t, "GET", f.path(), nil, "", false), 200)
	if initial["enabled"] != false || initial["version"] != float64(0) {
		t.Fatal(initial)
	}
	body := map[string]any{"mode": "enforce", "max_age": 300, "version": 0, "confirm": true}
	decodeTransport(t, f.call(t, "PUT", f.path(), body, "", true), 409)
	body["mode"] = "testing"
	decodeTransport(t, f.call(t, "PUT", f.path(), body, "", false), 403)
	current := decodeTransport(t, f.call(t, "PUT", f.path(), body, "", true), 200)
	if current["mode"] != "testing" || len(current["policy_id"].(string)) != 32 || !strings.HasPrefix(current["report_uri"].(string), "https://mail.platform.test/") {
		t.Fatal(current)
	}
	for _, host := range []string{"transport.example.test", "mta-sts.foreign.test", "mail.platform.test"} {
		response := f.call(t, "GET", "/.well-known/mta-sts.txt", nil, host, false)
		if response.Code != 404 {
			t.Fatalf("host bound route %s %d", host, response.Code)
		}
	}
	policy := f.call(t, "GET", "/.well-known/mta-sts.txt", nil, "mta-sts.transport.example.test", false)
	if policy.Code != 200 || policy.Body.String() != current["policy_body"] || !strings.Contains(policy.Body.String(), "mode: testing") {
		t.Fatalf("policy %d %s", policy.Code, policy.Body)
	}
	body["version"] = 1
	body["mode"] = "enforce"
	decodeTransport(t, f.call(t, "PUT", f.path(), body, "", true), 409)
	// This fixture supplies recorded observation facts; real DNS/TLS probing is
	// tested separately. HTTP status alone never manufactures verified evidence.
	row, err := store.New(f.platform).GetDomainTransportSecurity(ctx, store.GetDomainTransportSecurityParams{TenantID: f.tenant, DomainID: f.domain})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.platform.Exec(ctx, "UPDATE domain_transport_security SET testing_since=now()-interval '1 day',observation_status='verified',observed_at=now(),observation_digest=$3 WHERE tenant_id=$1 AND domain_id=$2", f.tenant, f.domain, mailtransport.Publication(row).Digest()); err != nil {
		t.Fatal(err)
	}
	enforced := decodeTransport(t, f.call(t, "PUT", f.path(), body, "", true), 200)
	if enforced["mode"] != "enforce" {
		t.Fatal(enforced)
	}
	decodeTransport(t, f.call(t, "PUT", f.path(), body, "", true), 409)
	body["mode"] = "none"
	body["max_age"] = 604800
	body["version"] = 2
	none := decodeTransport(t, f.call(t, "PUT", f.path(), body, "", true), 200)
	if none["mode"] != "none" {
		t.Fatal(none)
	}
	body["version"] = 3
	body["max_age"] = 300
	shortened := decodeTransport(t, f.call(t, "PUT", f.path(), body, "", true), 200)
	expiry, err := time.Parse(time.RFC3339Nano, shortened["cache_safe_after"].(string))
	if err != nil || expiry.Before(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("earlier longer cache lost %v %v", shortened, err)
	}
	decodeTransport(t, f.call(t, "DELETE", f.path(), map[string]any{"confirm": true, "version": 4}, "", true), 409)
	if _, err = f.platform.Exec(ctx, "UPDATE domain_transport_security SET cache_safe_after=now()-interval '1 second' WHERE tenant_id=$1 AND domain_id=$2", f.tenant, f.domain); err != nil {
		t.Fatal(err)
	}
	if response := f.call(t, "DELETE", f.path(), map[string]any{"confirm": true, "version": 4}, "", true); response.Code != 204 {
		t.Fatalf("drained disable %d %s", response.Code, response.Body)
	}
	if response := f.call(t, "GET", "/.well-known/mta-sts.txt", nil, "mta-sts.transport.example.test", false); response.Code != 404 {
		t.Fatal("disabled policy still served")
	}
}
func TestTLSReportIntakeBoundedDedupAndTenantIsolationPG(t *testing.T) {
	f := newTransportFixture(t)
	ctx := t.Context()
	decodeTransport(t, f.call(t, "PUT", f.path(), map[string]any{"mode": "testing", "max_age": 300, "version": 0, "confirm": true}, "", true), 200)
	now := time.Now().UTC()
	report := map[string]any{"organization-name": "unverified claimed sender", "contact-info": "private-contact@example.test", "report-id": "report-1", "date-range": map[string]string{"start-datetime": now.Add(-time.Hour).Format(time.RFC3339), "end-datetime": now.Format(time.RFC3339)}, "policies": []any{map[string]any{"policy": map[string]any{"policy-type": "sts", "policy-domain": "transport.example.test"}, "summary": map[string]int64{"total-successful-session-count": 10, "total-failure-session-count": 2}, "failure-details": []any{map[string]any{"result-type": "certificate-expired", "failed-session-count": 2, "receiving-ip": "192.0.2.7", "additional-information": "private free text"}}}}}
	raw, _ := json.Marshal(report)
	post := func(data []byte, media string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, "POST", "/api/v1/tls-reports/transport.example.test", bytes.NewReader(data))
		req.Header.Set("Content-Type", media)
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, req)
		return w
	}
	for range 2 {
		if w := post(raw, "application/tlsrpt+json"); w.Code != 202 {
			t.Fatalf("report %d %s", w.Code, w.Body)
		}
	}
	changed := bytes.Replace(raw, []byte(`"total-successful-session-count":10`), []byte(`"total-successful-session-count":11`), 1)
	if w := post(changed, "application/tlsrpt+json"); w.Code != 409 {
		t.Fatalf("conflicting same report id %d", w.Code)
	}
	var compressed bytes.Buffer
	zip := gzip.NewWriter(&compressed)
	_, _ = zip.Write(raw)
	_ = zip.Close()
	if w := post(compressed.Bytes(), "application/tlsrpt+gzip"); w.Code != 202 {
		t.Fatal(w.Code)
	}
	listed := decodeTransport(t, f.call(t, "GET", f.path()+"/reports", nil, "", false), 200)
	reports := listed["reports"].([]any)
	if len(reports) != 1 {
		t.Fatalf("duplicate reports %+v", reports)
	}
	safe, _ := json.Marshal(reports)
	for _, banned := range []string{"private-contact", "192.0.2.7", "private free text", "unverified claimed sender"} {
		if bytes.Contains(safe, []byte(banned)) {
			t.Fatal("raw report detail leaked")
		}
	}
	item := reports[0].(map[string]any)
	if item["source_verified"] != false || item["failed"] != float64(2) {
		t.Fatal(item)
	}
	if w := post(bytes.ReplaceAll(raw, []byte("transport.example.test"), []byte("other.example.test")), "application/tlsrpt+json"); w.Code != 422 {
		t.Fatal("cross-domain report accepted")
	}
	var bomb bytes.Buffer
	zip = gzip.NewWriter(&bomb)
	_, _ = zip.Write(bytes.Repeat([]byte("x"), 5<<20))
	_ = zip.Close()
	if w := post(bomb.Bytes(), "application/tlsrpt+gzip"); w.Code != 422 {
		t.Fatalf("zip expansion %d", w.Code)
	}
	var count int
	if err := f.platform.QueryRow(ctx, "SELECT count(*) FROM domain_tls_reports WHERE tenant_id=$1 AND domain_id=$2", f.tenant, f.domain).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stored %d %v", count, err)
	}
	tx, err := f.app.TenantTx(ctx, f.tenant+1000)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM domain_tls_reports").Scan(&count); err != nil || count != 0 {
		t.Fatalf("cross-tenant report RLS %d %v", count, err)
	}
	down, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0144_transport_policy_reports.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := f.platform.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback.Rollback(ctx)
	if _, err = rollback.Exec(ctx, string(down)); err == nil {
		t.Fatal("down discarded active policy/report facts")
	}
}

// TestTransportSecurityBrowserFixture exposes the same production auth/CSRF/API
// to an explicitly requested loopback browser. Public DNS remains genuinely
// unverified for the synthetic .test domain; no mock can authorize enforcement.
func TestTransportSecurityBrowserFixture(t *testing.T) {
	directory := os.Getenv("URMAIL_TRANSPORT_BROWSER_DIR")
	if directory == "" {
		t.Skip("private browser fixture not requested")
	}
	f := newTransportFixture(t)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.engine)
	defer server.Close()
	fixture, _ := json.Marshal(map[string]any{"url": server.URL, "email": "transport-owner@example.test", "password": "Synthetic-transport-owner!42", "domain_id": f.domain, "domain": "transport.example.test"})
	if err := os.WriteFile(filepath.Join(directory, "fixture.json"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		if result, err := os.ReadFile(filepath.Join(directory, "browser.done")); err == nil {
			if strings.TrimSpace(string(result)) != "PASS" {
				t.Fatal("browser failed")
			}
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	t.Fatal("transport browser timed out")
}
