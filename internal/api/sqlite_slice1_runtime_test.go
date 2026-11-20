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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/handlers"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
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
	// One-off migration path (test scaffolding role = controlled urmailctl).
	for _, name := range []string{"0001_init.sql", "0002_write_side_effects.sql"} {
		body, readErr := os.ReadFile(filepath.Join("..", "..", "migrations_sqlite", name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if _, execErr := db.ExecContext(ctx, string(body)); execErr != nil {
			t.Fatalf("apply %s: %v", name, execErr)
		}
	}
	// The controlled migrator sets user_version outside the file transaction
	// (an in-file PRAGMA is a no-op there); mirror that one-off behavior.
	if _, execErr := db.ExecContext(ctx, "PRAGMA user_version = 2"); execErr != nil {
		t.Fatalf("stamp user_version: %v", execErr)
	}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("schema version=%d err=%v, want 2", version, err)
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
		       (12,8,32,'bob','bob@b.example.test',` + hashSQL + `,4096,0,'active',1)`,
		// The usage-defaults trigger resets a non-zero bytes_used on INSERT
		// (usage must be measured); model a measured mailbox with an UPDATE.
		`UPDATE mailboxes SET bytes_used=2048 WHERE id=11`,
		// Reviewed sender grant: alice may send From the team alias; bob has none.
		`INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,kind,active,sender_logins)
		 VALUES(7,31,'team','alice@a.example.test','alias',1,'["alice@a.example.test"]')`,
	} {
		if _, execErr := db.ExecContext(ctx, stmt); execErr != nil {
			t.Fatalf("fixture %q: %v", stmt, execErr)
		}
	}

	engine := api.New(api.Options{
		SQLiteStore:   db,
		WebmailNative: true,
		DevMode:       true,
		MyHostname:    "localhost",
		IMAPAddr:      "127.0.0.1:1", // unreachable by construction: slice 1 never dials IMAP
		SMTPAddr:      "127.0.0.1:1",
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
	c.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, c.server.URL+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatalf("request: %v", err)
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
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_settings`).Scan(&settingsRows); err != nil || settingsRows != 2 {
		t.Fatalf("sqlite settings rows = %d (err=%v), want alice+bob", settingsRows, err)
	}
}
