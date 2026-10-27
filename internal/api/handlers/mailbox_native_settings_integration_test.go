package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// Mailbox A must never reach mailbox B's settings, quota or identities:
// every self-service statement is bound to the bearer session principal.
func TestMailboxSettingsIsolationQuotaAndIdentitiesPG(t *testing.T) {
	p, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	q := store.New(p)
	var tenant int64
	if err := p.QueryRow(ctx, `WITH b AS(INSERT INTO brands(slug,name)VALUES('self-settings','Self')RETURNING id),t AS(INSERT INTO tenants(brand_id,name,slug,status)SELECT id,'Self','self-settings','active' FROM b RETURNING id)SELECT id FROM t`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	var domain int64
	if err := p.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,provisioning_state)VALUES($1,'self.example.test','active','fixture',now(),'ready')RETURNING id`, tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	insertMailbox := func(local string) int64 {
		var id int64
		if err := p.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,bytes_used)VALUES($1,$2,$3,$3||'@self.example.test','fixture',8192,2048)RETURNING id`, tenant, domain, local).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	alice := insertMailbox("alice")
	bob := insertMailbox("bob")
	var alias int64
	if err := p.QueryRow(ctx, `INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,kind,active,sender_logins)VALUES($1,$2,'team','alice@self.example.test','alias',true,ARRAY['alice@self.example.test'])RETURNING id`, tenant, domain).Scan(&alias); err != nil {
		t.Fatal(err)
	}
	_ = alias

	if _, err := q.PlatformInsertMailboxSettings(ctx, store.PlatformInsertMailboxSettingsParams{
		TenantID:      tenant,
		MailboxID:     alice,
		DisplayName:   "Alice",
		SignatureText: "alice sig",
		SignatureHtml: "<b>alice sig</b>",
		Preferences:   []byte(`{"tz":"Asia/Shanghai"}`),
	}); err != nil {
		t.Fatal(err)
	}

	session := func(token, local string, id int64) middleware.MailboxSessionLookup {
		hash := mailboxprincipal.HashToken(token)
		return &fakeMailboxSessions{byHash: map[string]webmail.Session{hash: {TokenHash: hash, TenantID: tenant, MailboxID: id, Address: local + "@self.example.test", ExpiresAt: time.Now().Add(time.Hour)}}}
	}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(session("alice-token-0001", "alice", alice)))
	auth.GET("/settings", (&MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: &fakeMailboxSessions{byHash: map[string]webmail.Session{}}, SelfSettings: StoreMailboxSelfSettings{Pool: p}}).GetSettings)
	auth.PUT("/settings", (&MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: &fakeMailboxSessions{byHash: map[string]webmail.Session{}}, SelfSettings: StoreMailboxSelfSettings{Pool: p}}).PutSettings)
	auth.GET("/quota", (&MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: &fakeMailboxSessions{byHash: map[string]webmail.Session{}}, SelfSettings: StoreMailboxSelfSettings{Pool: p}}).GetQuota)
	auth.GET("/identities", (&MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: &fakeMailboxSessions{byHash: map[string]webmail.Session{}}, SelfSettings: StoreMailboxSelfSettings{Pool: p}}).ListIdentities)

	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer alice-token-0001")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := call(http.MethodGet, "/api/v1/mailbox/settings", "")
	var settings struct {
		DisplayName string          `json:"display_name"`
		Version     int64           `json:"version"`
		Preferences json.RawMessage `json:"preferences"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &settings); err != nil || settings.DisplayName != "Alice" || settings.Version != 1 {
		t.Fatalf("alice settings %d %s %v", w.Code, w.Body.String(), err)
	}

	// Optimistic conflict: version 99 does not exist.
	w = call(http.MethodPut, "/api/v1/mailbox/settings", `{"display_name":"Hijack","expected_version":99}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("conflict %d %s", w.Code, w.Body.String())
	}
	// Optimistic success advances the stored version.
	w = call(http.MethodPut, "/api/v1/mailbox/settings", `{"display_name":"Alice Two","expected_version":1}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}

	// Server-authoritative quota math.
	w = call(http.MethodGet, "/api/v1/mailbox/quota", "")
	var quota struct {
		UsedBytes  int64 `json:"used_bytes"`
		QuotaBytes int64 `json:"quota_bytes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &quota); err != nil || quota.QuotaBytes != 8192 {
		t.Fatalf("quota %d %s %v", w.Code, w.Body.String(), err)
	}
	// used_bytes echoes the server-stored usage exactly.
	var storedUsed int64
	if err := p.QueryRow(ctx, "SELECT bytes_used FROM mailboxes WHERE tenant_id=$1 AND id=$2", tenant, alice).Scan(&storedUsed); err != nil {
		t.Fatal(err)
	}
	if quota.UsedBytes != storedUsed {
		t.Fatalf("used bytes %d != stored %d", quota.UsedBytes, storedUsed)
	}

	// Authorized sending identities: primary plus the reviewed team alias.
	w = call(http.MethodGet, "/api/v1/mailbox/identities", "")
	var ids struct {
		Identities []struct {
			Address string `json:"address"`
			Primary bool   `json:"primary"`
		} `json:"identities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ids); err != nil || len(ids.Identities) != 2 {
		t.Fatalf("identities %d %s %v", w.Code, w.Body.String(), err)
	}

	// Bob's session must see empty defaults, never Alice's row, and cannot
	// write over her version.
	bobHandler := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: &fakeMailboxSessions{byHash: map[string]webmail.Session{}}, SelfSettings: StoreMailboxSelfSettings{Pool: p}}
	bobRouter := gin.New()
	bobAuth := bobRouter.Group("/api/v1/mailbox")
	bobAuth.Use(middleware.RequireMailboxAuth(session("bob-token-0001", "bob", bob)))
	bobAuth.GET("/settings", bobHandler.GetSettings)
	bobAuth.PUT("/settings", bobHandler.PutSettings)
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/mailbox/settings", nil)
	req.Header.Set("Authorization", "Bearer bob-token-0001")
	w = httptest.NewRecorder()
	bobRouter.ServeHTTP(w, req)
	var bobSettings struct {
		Version     int64  `json:"version"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &bobSettings); err != nil || bobSettings.Version != 0 || bobSettings.DisplayName != "" {
		t.Fatalf("bob must see defaults, got %d %s %v", w.Code, w.Body.String(), err)
	}
	req = httptest.NewRequestWithContext(ctx, http.MethodPut, "/api/v1/mailbox/settings", bytes.NewReader([]byte(`{"display_name":"Eve","expected_version":2}`)))
	req.Header.Set("Authorization", "Bearer bob-token-0001")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	bobRouter.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("bob must not write alice's settings row")
	}

}
