package api_test

// Slice-1 SQLite runtime candidate (V2 contract §5): REAL composition.
// api.New wires the mailbox login/session + self-settings surface to the
// authoritative SQLite file (sqlitedb.Open) with NO PostgreSQL pool in the
// process, then the test drives the real HTTP handlers: login → read/write
// settings → invalid/revoked session rejection → cross-mailbox and
// cross-tenant isolation → zero PG fallback.
//
// The schema is applied here by the test scaffolding exactly like the
// controlled one-off `urmailctl migrate` would; the application itself never
// migrates or creates the database file (the store/router diff contains no
// Migrate call — see the receipt evidence).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/db"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/imageproxy"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

func newSlice1Server(t *testing.T) (*sql.DB, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "slice1.db")
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlitedb.Migrate(ctx, db, os.DirFS(filepath.Join("..", "..")), "migrations_sqlite"); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != sqlitedb.ExpectedSchemaVersion {
		t.Fatalf("schema version=%d err=%v, want %d", version, err, sqlitedb.ExpectedSchemaVersion)
	}

	password := "slice1-mailbox-password"
	hash, hashErr := domainMail.HashPassword(password)
	if hashErr != nil {
		t.Fatalf("hash password: %v", hashErr)
	}
	hashSQL := "'" + strings.ReplaceAll(hash, "'", "''") + "'"
	for _, stmt := range []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'slice1','Slice1')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(7,1,'AliceTenant','alice-tenant','active')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(8,1,'BobTenant','bob-tenant','active')`,
		`INSERT INTO domains(id,tenant_id,fqdn,status,verification_token,verified_at,provisioning_state)
		 VALUES(31,7,'a.example.test','active','tok',1737412800123456,'pending'),
		        (32,8,'b.example.test','active','tok',1737412800123456,'pending')`,
		// domain_provisioning_insert_pending forces a pending state; only an
		// explicit UPDATE models an already-provisioned domain (same as the
		// claim/store fixtures).
		`UPDATE domains SET provisioning_state='ready' WHERE id IN (31,32)`,
		`INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash,quota_bytes,bytes_used,status,send_enabled)
		 VALUES(11,7,31,'alice','alice@a.example.test',` + hashSQL + `,8192,2048,'active',1),
		       (12,8,32,'bob','bob@b.example.test',` + hashSQL + `,4096,0,'active',1),
		       (13,7,31,'carol','carol@a.example.test',` + hashSQL + `,4096,0,'active',1)`,
		// The usage-defaults trigger resets a non-zero bytes_used on INSERT
		// (usage must be measured); model a measured mailbox with an UPDATE.
		`UPDATE mailboxes SET bytes_used=2048 WHERE id=11`,
		// Reviewed sender grant: alice may send From the team alias; bob has none.
		`INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,kind,active,sender_logins)
		 VALUES(7,31,'team','alice@a.example.test','alias',1,'["alice@a.example.test"]')`,
		// Dual-brand fixtures: acme (active brand host), disb (failed host),
		// plus a pending-verify host on the healthy brand. The lifecycle
		// columns exist in the SQLite schema, so the boundary is fully
		// testable without a fail-closed gap.
		`INSERT INTO brands(id,slug,name,status) VALUES(9,'acme','Acme','active'),(10,'disb','Disabled','disabled')`,
		`INSERT INTO brand_hosts(hostname,brand_id,status)
		 VALUES('acme.example.test',9,'active'),
		       ('dead.example.test',10,'failed'),
		       ('pend.example.test',9,'pending-verify')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(14,9,'AcmeTenant','acme-tenant','active')`,
		`INSERT INTO domains(id,tenant_id,fqdn,status,verification_token,verified_at,provisioning_state)
		 VALUES(33,14,'d.acme.example.test','active','tok',1737412800123456,'pending')`,
		`UPDATE domains SET provisioning_state='ready' WHERE id=33`,
		`INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash,quota_bytes,bytes_used,status,send_enabled)
		 VALUES(21,14,33,'dave','dave@d.acme.example.test',` + hashSQL + `,4096,0,'active',1)`,
	} {
		if _, execErr := db.ExecContext(ctx, stmt); execErr != nil {
			t.Fatalf("fixture %q: %v", stmt, execErr)
		}
	}

	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatalf("load embedded locale catalog for fixture: %v", err)
	}
	engine := api.New(api.Options{
		SQLiteStore:      db,
		Messages:         messages,
		WebmailNative:    true,
		DevMode:          true,
		MyHostname:       "localhost",
		IMAPAddr:         "127.0.0.1:1", // unreachable by construction: slice 1 never dials IMAP
		SMTPAddr:         "127.0.0.1:1",
		WebmailRenderKey: []byte("slice1-render-key"),
	})
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return db, server
}

type slice1Client struct {
	t      *testing.T
	server *httptest.Server
	token  string
}

func (c *slice1Client) do(method, path, body string) (int, string) {
	return c.doHost(method, path, body, "")
}

// doHost overrides the Host header (brand-host boundary testing).
func (c *slice1Client) doHost(method, path, body, host string) (int, string) {
	c.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, c.server.URL+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatalf("request: %v", err)
	}
	if host != "" {
		req.Host = host
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(raw)
}

type settingsDTO struct {
	DisplayName string          `json:"display_name"`
	Version     int64           `json:"version"`
	Preferences json.RawMessage `json:"preferences"`
}

func TestSlice1SQLiteMailboxRuntime(t *testing.T) {
	db, server := newSlice1Server(t)
	client := &slice1Client{t: t, server: server}
	ctx := context.Background()
	const password = "slice1-mailbox-password"

	// (a) unknown mailbox → original 401 semantics.
	client.token = ""
	if code, _ := client.do(http.MethodPost, "/api/v1/mailbox/session", `{"email":"ghost@a.example.test","password":"x"}`); code != http.StatusUnauthorized {
		t.Fatalf("unknown mailbox login = %d, want 401", code)
	}
	// (b) wrong password → 401.
	if code, _ := client.do(http.MethodPost, "/api/v1/mailbox/session", `{"email":"alice@a.example.test","password":"not-the-password"}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong password login = %d, want 401", code)
	}

	// (c) real login.
	code, body := client.do(http.MethodPost, "/api/v1/mailbox/session", `{"email":"alice@a.example.test","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login = %d %s", code, body)
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &session); err != nil || session.Token == "" {
		t.Fatalf("login body %q: %v", body, err)
	}
	client.token = session.Token

	// (d) default read: version 0, empty defaults.
	code, body = client.do(http.MethodGet, "/api/v1/mailbox/settings", "")
	if code != http.StatusOK {
		t.Fatalf("get settings = %d %s", code, body)
	}
	var settings settingsDTO
	if err := json.Unmarshal([]byte(body), &settings); err != nil || settings.Version != 0 || settings.DisplayName != "" {
		t.Fatalf("default settings = %s (%v)", body, err)
	}

	// (e) optimistic write through the real handler.
	code, body = client.do(http.MethodPut, "/api/v1/mailbox/settings",
		`{"display_name":"Alice A.","signature_text":"alice sig","expected_version":0}`)
	if code != http.StatusOK {
		t.Fatalf("put settings = %d %s", code, body)
	}
	// (f) read-back shows the saved values and version 1.
	code, body = client.do(http.MethodGet, "/api/v1/mailbox/settings", "")
	if code != http.StatusOK {
		t.Fatalf("get settings after save = %d %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &settings); err != nil || settings.DisplayName != "Alice A." || settings.Version != 1 {
		t.Fatalf("saved settings = %s (%v)", body, err)
	}

	// (g) server-authoritative quota.
	code, body = client.do(http.MethodGet, "/api/v1/mailbox/quota", "")
	var quota struct {
		UsedBytes  int64 `json:"used_bytes"`
		QuotaBytes int64 `json:"quota_bytes"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &quota) != nil || quota.UsedBytes != 2048 || quota.QuotaBytes != 8192 {
		t.Fatalf("quota = %d %s", code, body)
	}

	// (h) authorized identities: primary + reviewed team alias (grant scope).
	code, body = client.do(http.MethodGet, "/api/v1/mailbox/identities", "")
	var ids struct {
		Identities []struct {
			Address string `json:"address"`
		} `json:"identities"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &ids) != nil || len(ids.Identities) != 2 {
		t.Fatalf("identities = %d %s", code, body)
	}

	// (h2) slice-1 gate: with a VALID session, every non-slice mailbox route
	// must answer 503 FEATURE_UNAVAILABLE BEFORE any handler side effect —
	// no IMAP dial (that would be 502 IMAP_UNAVAILABLE), no SMTP submit, no
	// storage write. Gate runs after authentication, so these all carry the
	// valid bearer.
	var scheduledRows int
	denyProbes := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/v1/mailbox/folders", `{"name":"Custom"}`},
		{http.MethodPost, "/api/v1/mailbox/send", `{"to":["bob@b.example.test"],"text":"hi"}`},
		{http.MethodGet, "/api/v1/mailbox/rules", ""},
		{http.MethodPost, "/api/v1/mailbox/scheduled-sends", `{"draft_uid":1,"draft_uidvalidity":1,"send_at":"2030-01-01T00:00:00Z"}`},
		{http.MethodPost, "/api/v1/mailbox/password", `{"old_password":"x","new_password":"y"}`},
	}
	for _, probe := range denyProbes {
		code, body = client.do(probe.method, probe.path, probe.body)
		if code != http.StatusServiceUnavailable || !strings.Contains(body, "FEATURE_UNAVAILABLE") {
			t.Fatalf("non-slice %s %s = %d %s, want 503 FEATURE_UNAVAILABLE", probe.method, probe.path, code, body)
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_scheduled_sends`).Scan(&scheduledRows); err != nil || scheduledRows != 0 {
		t.Fatalf("scheduled-sends rows = %d (err=%v), want 0: a denied route wrote storage", scheduledRows, err)
	}

	// (h3) OUT-OF-GROUP boundary (same single default-deny boundary): the
	// EAS alias authenticates through ProtocolAuth and the image proxy is
	// authorized by signed URL — both bypass mailboxAuthed. In SQLite mode
	// protocol auth must be side-effect-free (NO mailbox_sessions row) and
	// the gate must answer 503 before any provider/network work.
	rawRequest := func(method, path string, basic bool, query url.Values) (int, string) {
		t.Helper()
		target := server.URL + path
		if len(query) > 0 {
			target += "?" + query.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, method, target, nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if basic {
			req.SetBasicAuth("alice@a.example.test", password)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do %s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp.StatusCode, string(raw)
	}
	var sessionsBefore, sessionsAfter int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_sessions`).Scan(&sessionsBefore); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	// Valid Basic credentials: validation passes (rate limiter counts a
	// success), but NO session row is written and EAS answers
	// FEATURE_UNAVAILABLE — never the 200/501 protocol handler.
	for _, easPath := range []string{"/api/v1/eas", "/Microsoft-Server-ActiveSync"} {
		code, body = rawRequest(http.MethodPost, easPath, true, nil)
		if code != http.StatusServiceUnavailable || !strings.Contains(body, "FEATURE_UNAVAILABLE") {
			t.Fatalf("EAS Basic %s = %d %s, want 503 FEATURE_UNAVAILABLE", easPath, code, body)
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_sessions`).Scan(&sessionsAfter); err != nil || sessionsAfter != sessionsBefore {
		t.Fatalf("EAS Basic mutated sessions: before=%d after=%d (err=%v)", sessionsBefore, sessionsAfter, err)
	}
	// Invalid Basic credentials keep the protocol's 401 semantics (and write
	// nothing).
	code, body = rawRequest(http.MethodPost, "/api/v1/eas", false, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("EAS anonymous = %d %s, want 401", code, body)
	}
	req401 := func() (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/eas", nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		req.SetBasicAuth("alice@a.example.test", "not-the-password")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp.StatusCode, string(raw)
	}
	if code, body = req401(); code != http.StatusUnauthorized {
		t.Fatalf("EAS wrong password = %d %s, want 401", code, body)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_sessions`).Scan(&sessionsAfter); err != nil || sessionsAfter != sessionsBefore {
		t.Fatalf("EAS failures mutated sessions: before=%d after=%d (err=%v)", sessionsBefore, sessionsAfter, err)
	}
	// Image proxy: a VALID signed URL must not reach the fetcher — the gate
	// answers 503 before signature verification, so no external request is
	// even attempted (the target below is unreachable loopback port 1; a
	// handler call would have degraded to a 200 placeholder instead).
	signer := imageproxy.NewSigner([]byte("slice1-render-key"))
	token, signature, signErr := signer.Sign(imageproxy.Payload{MailboxID: 11, UID: 1, URL: "http://127.0.0.1:1/pixel.png"})
	if signErr != nil {
		t.Fatalf("sign payload: %v", signErr)
	}
	code, body = rawRequest(http.MethodGet, "/api/v1/mailbox/images/proxy", false, url.Values{"u": {token}, "sig": {signature}})
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "FEATURE_UNAVAILABLE") {
		t.Fatalf("image proxy = %d %s, want 503 FEATURE_UNAVAILABLE", code, body)
	}

	// snapshot alice's row before the isolation negatives.
	type snap struct {
		name      string
		version   int64
		updatedAt int64
	}
	readAlice := func() snap {
		t.Helper()
		var s snap
		if err := db.QueryRowContext(ctx, `SELECT display_name,version,updated_at FROM mailbox_settings WHERE tenant_id=7 AND mailbox_id=11`).
			Scan(&s.name, &s.version, &s.updatedAt); err != nil {
			t.Fatalf("snapshot alice: %v", err)
		}
		return s
	}
	aliceBefore := readAlice()

	// (i) garbage bearer → 401, and no settings write happened.
	savedToken := client.token
	client.token = "definitely-not-a-token"
	if code, _ := client.do(http.MethodGet, "/api/v1/mailbox/settings", ""); code != http.StatusUnauthorized {
		t.Fatalf("garbage bearer = %d, want 401", code)
	}
	// (j) no bearer at all → 401.
	client.token = ""
	if code, _ := client.do(http.MethodGet, "/api/v1/mailbox/settings", ""); code != http.StatusUnauthorized {
		t.Fatalf("missing bearer = %d, want 401", code)
	}
	client.token = savedToken

	// (k) logout revokes the session; afterwards the same bearer is 401 and
	// the row carries a physical revoked_at (never deleted).
	code, body = client.do(http.MethodDelete, "/api/v1/mailbox/session", "")
	if code != http.StatusOK {
		t.Fatalf("logout = %d %s", code, body)
	}
	if code, _ := client.do(http.MethodGet, "/api/v1/mailbox/settings", ""); code != http.StatusUnauthorized {
		t.Fatalf("post-logout settings = %d, want 401", code)
	}
	tokenHash := mailboxprincipal.HashToken(savedToken)
	var revoked sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT revoked_at FROM mailbox_sessions WHERE token_hash=?`, tokenHash).Scan(&revoked); err != nil || !revoked.Valid {
		t.Fatalf("revoked_at not set (err=%v, value=%v)", err, revoked)
	}
	var liveSessions int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_sessions WHERE token_hash=? AND revoked_at IS NULL`, tokenHash).Scan(&liveSessions); err != nil || liveSessions != 0 {
		t.Fatalf("live sessions after logout = %d (err=%v)", liveSessions, err)
	}

	// (l) bob (other tenant) logs in: reads his own defaults, never alice's row.
	bob := &slice1Client{t: t, server: server}
	code, body = bob.do(http.MethodPost, "/api/v1/mailbox/session", `{"email":"bob@b.example.test","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("bob login = %d %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &session); err != nil || session.Token == "" {
		t.Fatalf("bob login body %q", body)
	}
	bob.token = session.Token
	code, body = bob.do(http.MethodGet, "/api/v1/mailbox/settings", "")
	if code != http.StatusOK {
		t.Fatalf("bob settings = %d %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &settings); err != nil || settings.Version != 0 || settings.DisplayName == "Alice A." {
		t.Fatalf("bob must not see alice's settings, got %s", body)
	}
	// bob writes his own row; alice's row must stay identical.
	code, body = bob.do(http.MethodPut, "/api/v1/mailbox/settings", `{"display_name":"Bob B.","expected_version":0}`)
	if code != http.StatusOK {
		t.Fatalf("bob put = %d %s", code, body)
	}
	aliceAfter := readAlice()
	if aliceBefore != aliceAfter {
		t.Fatalf("alice row changed by bob's write: before=%+v after=%+v", aliceBefore, aliceAfter)
	}

	// (l2) SAME-TENANT other mailbox: carol sits in alice's tenant 7. Her
	// session must expose only her own defaults; her write must not touch
	// alice's row — the handler derives (tenant, mailbox) from the session
	// principal, and there is no client-supplied mailbox parameter.
	carol := &slice1Client{t: t, server: server}
	code, body = carol.do(http.MethodPost, "/api/v1/mailbox/session", `{"email":"carol@a.example.test","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("carol login = %d %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &session); err != nil || session.Token == "" {
		t.Fatalf("carol login body %q", body)
	}
	carol.token = session.Token
	code, body = carol.do(http.MethodGet, "/api/v1/mailbox/settings", "")
	if code != http.StatusOK {
		t.Fatalf("carol settings = %d %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &settings); err != nil || settings.Version != 0 || settings.DisplayName == "Alice A." {
		t.Fatalf("carol must not see alice's settings, got %s", body)
	}
	code, body = carol.do(http.MethodPut, "/api/v1/mailbox/settings", `{"display_name":"Carol C.","expected_version":0}`)
	if code != http.StatusOK {
		t.Fatalf("carol put = %d %s", code, body)
	}
	aliceAfterCarol := readAlice()
	if aliceBefore != aliceAfterCarol {
		t.Fatalf("alice row changed by same-tenant carol write: before=%+v after=%+v", aliceBefore, aliceAfterCarol)
	}

	// (l3) REAL expired session (DB-level expiry, not logout revocation):
	// rewind carol's session expiry into the past; the same bearer must
	// become 401 while the physical row stays (nothing deleted).
	carolHash := mailboxprincipal.HashToken(carol.token)
	if _, err := db.ExecContext(ctx,
		`UPDATE mailbox_sessions SET expires_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)-1000 WHERE token_hash=?`,
		carolHash); err != nil {
		t.Fatalf("expire carol session: %v", err)
	}
	if code, _ := carol.do(http.MethodGet, "/api/v1/mailbox/settings", ""); code != http.StatusUnauthorized {
		t.Fatalf("expired session settings = %d, want 401", code)
	}
	var expiredRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_sessions WHERE token_hash=?`, carolHash).Scan(&expiredRows); err != nil || expiredRows != 1 {
		t.Fatalf("expired session row missing: rows=%d err=%v", expiredRows, err)
	}

	// (m) adapter-misuse probes: wrong tenant/mailbox binding must neither
	// read nor write alice's row. The probe reuses the production adapter
	// itself, so the SQL predicates are the thing under test.
	probe := handlers.SQLiteMailboxStore{DB: db}
	if leaked, err := probe.Settings(ctx, 8, 11); err != nil || leaked.Version != 0 || leaked.DisplayName == "Alice A." {
		t.Fatalf("cross-tenant read touched alice row: %+v (err=%v)", leaked, err)
	}
	if _, err := probe.SaveSettings(ctx, handlers.MailboxSettingsSave{TenantID: 8, MailboxID: 11, DisplayName: "Hijack", ExpectedVersion: aliceAfter.version}); err == nil {
		t.Fatal("cross-tenant write with alice's version succeeded")
	}
	if _, err := probe.SaveSettings(ctx, handlers.MailboxSettingsSave{TenantID: 7, MailboxID: 12, DisplayName: "Hijack", ExpectedVersion: 99}); err == nil {
		t.Fatal("cross-mailbox write with bogus version succeeded")
	}
	aliceFinal := readAlice()
	if aliceFinal != aliceAfter {
		t.Fatalf("alice row mutated by hijack probes: want %+v got %+v", aliceAfter, aliceFinal)
	}

	// (n) missing scope: bob's tenant has no sender grant for the alias and
	// bob has no grant at all; the adapter must answer false with no rows.
	if ok, err := probe.SenderIdentityAllowed(ctx, 8, "alice@a.example.test", "team@a.example.test"); err != nil || ok {
		t.Fatalf("cross-tenant sender grant allowed (ok=%v err=%v)", ok, err)
	}
	if ok, err := probe.SenderIdentityAllowed(ctx, 7, "stranger@a.example.test", "team@a.example.test"); err != nil || ok {
		t.Fatalf("sender without grant allowed (ok=%v err=%v)", ok, err)
	}

	// (o) zero PG fallback, structural: no PostgreSQL pool was ever opened in
	// this process (Options.PlatformPool stayed nil), yet every slice route
	// above served from the SQLite file — the asserted rows live there.
	var settingsRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_settings`).Scan(&settingsRows); err != nil || settingsRows != 3 {
		t.Fatalf("sqlite settings rows = %d (err=%v), want alice+bob+carol", settingsRows, err)
	}
}

// TestSQLiteSingleEngineRejectsPostgreSQLPools pins the mutual-exclusion
// guard: configuring SQLiteStore together with ANY PostgreSQL pool must fail
// the composition — that combination is exactly how a feature could bypass
// the single authoritative store (contract V2 §1.9).
func TestSQLiteSingleEngineRejectsPostgreSQLPools(t *testing.T) {
	// A *db.Pool cannot be constructed outside package db; a non-nil pointer
	// to the zero value is enough to trip the guard, which only checks nil.
	fakePool := reflect.New(reflect.TypeOf((*db.Pool)(nil)).Elem()).Interface().(*db.Pool)
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(fmt.Sprint(r), "mutually exclusive") {
			t.Fatalf("want single-engine panic, got %v", r)
		}
	}()
	_ = api.New(api.Options{SQLiteStore: &sql.DB{}, PlatformPool: fakePool})
	t.Fatal("api.New accepted SQLiteStore together with a PostgreSQL pool")
}

// TestSlice1SQLiteBrandHostBoundary exercises the dual-brand fixture over
// the real router: correct-brand login/self-settings work end to end,
// wrong-brand auth is rejected BEFORE a session row is issued, inactive /
// unverified brand hosts are rejected by the boundary itself, and the
// protocol Basic path keeps its brand boundary with zero session writes.
func TestSlice1SQLiteBrandHostBoundary(t *testing.T) {
	db, server := newSlice1Server(t)
	ctx := context.Background()
	const password = "slice1-mailbox-password"

	brandClient := &slice1Client{t: t, server: server}
	sessions := func() int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_sessions`).Scan(&n); err != nil {
			t.Fatalf("count sessions: %v", err)
		}
		return n
	}
	before := sessions()

	// Inactive / unverified brand hosts: the boundary rejects before any
	// authentication storage work, with the protocol's Misdirected Request.
	for _, host := range []string{"dead.example.test", "pend.example.test"} {
		brandClient.token = ""
		code, body := brandClient.doHost(http.MethodPost, "/api/v1/mailbox/session", `{"email":"dave@d.acme.example.test","password":"`+password+`"}`, host)
		if code != http.StatusMisdirectedRequest {
			t.Fatalf("login on %s = %d %s, want 421", host, code, body)
		}
	}
	if got := sessions(); got != before {
		t.Fatalf("inactive-host logins wrote sessions: before=%d after=%d", before, got)
	}

	// Wrong-brand login on a healthy brand host: rejected at the session
	// boundary (403), before Sessions.Create writes anything.
	brandClient.token = ""
	code, body := brandClient.doHost(http.MethodPost, "/api/v1/mailbox/session", `{"email":"alice@a.example.test","password":"`+password+`"}`, "acme.example.test")
	if code != http.StatusForbidden || !strings.Contains(body, "BRAND_HOST_TENANT_MISMATCH") {
		t.Fatalf("wrong-brand login = %d %s, want 403 BRAND_HOST_TENANT_MISMATCH", code, body)
	}
	if got := sessions(); got != before {
		t.Fatalf("wrong-brand login wrote sessions: before=%d after=%d", before, got)
	}

	// Correct-brand login: dave (tenant 14, brand 9) on acme.example.test.
	code, body = brandClient.doHost(http.MethodPost, "/api/v1/mailbox/session", `{"email":"dave@d.acme.example.test","password":"`+password+`"}`, "acme.example.test")
	if code != http.StatusOK {
		t.Fatalf("brand login = %d %s", code, body)
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &session); err != nil || session.Token == "" {
		t.Fatalf("brand login body %q", body)
	}
	brandClient.token = session.Token

	// Branded self-settings work end to end (RequireMailboxAuth brand check
	// passes; previously the missing interface failed every branded request).
	code, body = brandClient.doHost(http.MethodPut, "/api/v1/mailbox/settings", `{"display_name":"Dave D.","expected_version":0}`, "acme.example.test")
	if code != http.StatusOK {
		t.Fatalf("brand put settings = %d %s", code, body)
	}
	code, body = brandClient.doHost(http.MethodGet, "/api/v1/mailbox/settings", "", "acme.example.test")
	var settings settingsDTO
	if code != http.StatusOK || json.Unmarshal([]byte(body), &settings) != nil || settings.DisplayName != "Dave D." || settings.Version != 1 {
		t.Fatalf("brand settings read = %d %s", code, body)
	}

	// The branded session is origin-bound: the same bearer on the main host
	// must not be accepted.
	code, body = brandClient.doHost(http.MethodGet, "/api/v1/mailbox/settings", "", "localhost")
	if code != http.StatusUnauthorized {
		t.Fatalf("brand token on main host = %d %s, want 401", code, body)
	}

	// Protocol Basic on the brand host: side-effect-free validation plus the
	// brand boundary. Correct brand → gate 503 (no session row); wrong
	// brand → 403 (no session row).
	basicAuth := func(host, email string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/eas", nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		req.Host = host
		req.SetBasicAuth(email, password)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp.StatusCode, string(raw)
	}
	code, body = basicAuth("acme.example.test", "dave@d.acme.example.test")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "FEATURE_UNAVAILABLE") {
		t.Fatalf("brand EAS Basic = %d %s, want 503 FEATURE_UNAVAILABLE", code, body)
	}
	code, body = basicAuth("acme.example.test", "alice@a.example.test")
	if code != http.StatusForbidden {
		t.Fatalf("wrong-brand EAS Basic = %d %s, want 403", code, body)
	}
	if got := sessions(); got != before+1 {
		t.Fatalf("brand flow session rows: want exactly the one branded login (%d), got %d", before+1, got)
	}
}

// TestSlice1SQLiteBrandLookupFailureFailsClosed injects a real SQLite query
// failure at the brand boundary, proves no session or feature method runs,
// then restores the exact schema DDL and proves the same healthy brand
// login/settings path recovers. This pins error-before-found ordering
// against SQLite driver's (found=false, err!=nil) result shape.
func TestSlice1SQLiteBrandLookupFailureFailsClosed(t *testing.T) {
	db, server := newSlice1Server(t)
	ctx := context.Background()
	var brandHostsDDL string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='brand_hosts'`).Scan(&brandHostsDDL); err != nil || brandHostsDDL == "" {
		t.Fatalf("read brand_hosts DDL: ddl=%q err=%v", brandHostsDDL, err)
	}
	var before int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_sessions`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE brand_hosts`); err != nil {
		t.Fatalf("drop brand_hosts for failure injection: %v", err)
	}
	client := &slice1Client{t: t, server: server}
	code, body := client.doHost(http.MethodPost, "/api/v1/mailbox/session", `{"email":"dave@d.acme.example.test","password":"slice1-mailbox-password"}`, "acme.example.test")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "DB_ERROR") {
		t.Fatalf("failed brand lookup = %d %s, want fixed 503 DB_ERROR", code, body)
	}
	if strings.Contains(strings.ToLower(body), "no such table") || strings.Contains(body, "brand_hosts") || strings.Contains(body, "sqlite") {
		t.Fatalf("brand DB detail leaked: %s", body)
	}
	var afterFailure int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_sessions`).Scan(&afterFailure); err != nil || afterFailure != before {
		t.Fatalf("failed brand lookup changed session rows: before=%d after=%d err=%v", before, afterFailure, err)
	}

	// Restore precisely the schema emitted by the migration, then add the
	// healthy active host; no app-side migration path is involved.
	if _, err := db.ExecContext(ctx, brandHostsDDL); err != nil {
		t.Fatalf("restore brand_hosts DDL: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO brand_hosts(hostname,brand_id,status) VALUES('acme.example.test',9,'active')`); err != nil {
		t.Fatalf("restore healthy brand fixture: %v", err)
	}
	code, body = client.doHost(http.MethodPost, "/api/v1/mailbox/session", `{"email":"dave@d.acme.example.test","password":"slice1-mailbox-password"}`, "acme.example.test")
	if code != http.StatusOK {
		t.Fatalf("healthy brand login after restore = %d %s", code, body)
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &session); err != nil || session.Token == "" {
		t.Fatalf("restored brand login body %q", body)
	}
	client.token = session.Token
	code, body = client.doHost(http.MethodGet, "/api/v1/mailbox/settings", "", "acme.example.test")
	var settings settingsDTO
	if code != http.StatusOK || json.Unmarshal([]byte(body), &settings) != nil || settings.Version != 0 {
		t.Fatalf("restored brand settings = %d %s", code, body)
	}
}
