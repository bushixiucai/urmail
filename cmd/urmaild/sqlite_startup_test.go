package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/internal/store"
)

func seedMigratedSQLiteFixture(t *testing.T, dbPath string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open sqlite fixture: %v", err)
	}
	defer func() { _ = db.Close() }()

	migrations := os.DirFS(filepath.Join("..", "..", "migrations_sqlite"))
	if err := sqlitedb.Migrate(ctx, db, migrations, "."); err != nil {
		t.Fatalf("migrate sqlite fixture: %v", err)
	}

	hash, err := domainMail.HashPassword("daemon-sqlite-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	hashSQL := "'" + strings.ReplaceAll(hash, "'", "''") + "'"
	for _, stmt := range []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'slice1','Slice1')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(7,1,'AliceTenant','alice-tenant','active')`,
		`INSERT INTO domains(id,tenant_id,fqdn,status,verification_token,verified_at,provisioning_state)
		 VALUES(31,7,'a.example.test','active','tok',1737412800123456,'pending')`,
		`UPDATE domains SET provisioning_state='ready' WHERE id=31`,
		`INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash,quota_bytes,bytes_used,status,send_enabled)
		 VALUES(11,7,31,'alice','alice@a.example.test',` + hashSQL + `,8192,2048,'active',1)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed sqlite fixture %q: %v", stmt, err)
		}
	}
}

func TestDaemonSQLiteStartupHealthzAndSlice1Loop(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "urmail.sqlite")
	seedMigratedSQLiteFixture(t, dbPath)

	masterKey := filepath.Join(dir, "master.key")
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(masterKey, keyBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	envFile := filepath.Join(dir, "urmaild.env")
	envContent := "URMAIL_SQLITE_PATH=" + dbPath + "\n" +
		"URMAIL_DB_ENGINE=sqlite\n" +
		"URMAIL_POLICY_ENABLED=true\n" +
		"URMAIL_WEBMAIL_NATIVE=true\n" +
		"URMAIL_MASTER_KEY_PATH=" + masterKey + "\n" +
		"URMAIL_MYHOSTNAME=mail.a.example.test\n"
	if err := os.WriteFile(envFile, []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("URMAIL_ENV_FILE", envFile)
	for _, key := range []string{
		"URMAIL_DB_ENGINE",
		"URMAIL_SQLITE_PATH",
		"URMAIL_DATABASE_URL",
		"URMAIL_DATABASE_URL_PLATFORM",
		"URMAIL_DATABASE_URL_MIGRATE",
		"URMAIL_DATABASE_URL_DOVECOT",
		"URMAIL_POLICY_ENABLED",
		"URMAIL_WEBMAIL_NATIVE",
		"URMAIL_DEV_MODE",
	} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unsetenv %s: %v", key, err)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, cleanup, err := prepareDaemon(ctx, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("prepareDaemon: %v", err)
	}
	defer cleanup()

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	// 1. /api/v1/healthz reports 200 ok + db=ok in production SQLite mode.
	resp, err := func() (*http.Response, error) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/healthz", nil)
		return ts.Client().Do(req)
	}()
	if err != nil {
		t.Fatalf("GET /api/v1/healthz: %v", err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/v1/healthz = %d %s, want 200", resp.StatusCode, bodyBytes)
	}
	var health map[string]any
	if err := json.Unmarshal(bodyBytes, &health); err != nil || health["status"] != "ok" || health["db"] != "ok" || health["mode"] != "production" {
		t.Fatalf("unexpected healthz payload: %s", bodyBytes)
	}

	// 2. Slice-1 mailbox login + settings GET/PUT loop works through urmaild wiring.
	loginReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/v1/mailbox/session",
		strings.NewReader(`{"email":"alice@a.example.test","password":"daemon-sqlite-password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp, err := ts.Client().Do(loginReq)
	if err != nil {
		t.Fatalf("POST /api/v1/mailbox/session: %v", err)
	}
	loginBody, _ := io.ReadAll(loginResp.Body)
	_ = loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("mailbox login = %d %s", loginResp.StatusCode, loginBody)
	}
	var loginPayload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(loginBody, &loginPayload); err != nil || loginPayload.Token == "" {
		t.Fatalf("unexpected login response: %s", loginBody)
	}

	putReq, _ := http.NewRequestWithContext(ctx, http.MethodPut, ts.URL+"/api/v1/mailbox/settings",
		strings.NewReader(`{"display_name":"Alice Daemon","expected_version":0}`))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.Header.Set("Authorization", "Bearer "+loginPayload.Token)
	putResp, err := ts.Client().Do(putReq)
	if err != nil {
		t.Fatalf("PUT /api/v1/mailbox/settings: %v", err)
	}
	putBody, _ := io.ReadAll(putResp.Body)
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("put settings = %d %s", putResp.StatusCode, putBody)
	}

	getReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/mailbox/settings", nil)
	getReq.Header.Set("Authorization", "Bearer "+loginPayload.Token)
	getResp, err := ts.Client().Do(getReq)
	if err != nil {
		t.Fatalf("GET /api/v1/mailbox/settings: %v", err)
	}
	getBody, _ := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK || !strings.Contains(string(getBody), "Alice Daemon") {
		t.Fatalf("get settings = %d %s", getResp.StatusCode, getBody)
	}

	// 3. Unmigrated / PG-only routes stay explicitly fail-closed (no panic, no PG fallback).
	authReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/v1/auth/login",
		strings.NewReader(`{"email":"admin@example.test","password":"x"}`))
	authReq.Header.Set("Content-Type", "application/json")
	authResp, err := ts.Client().Do(authReq)
	if err != nil {
		t.Fatalf("POST /api/v1/auth/login: %v", err)
	}
	_ = authResp.Body.Close()
	if authResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("PG-only /api/v1/auth/login = %d, want 503 FEATURE_UNAVAILABLE", authResp.StatusCode)
	}

	folderReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/mailbox/folders", nil)
	folderReq.Header.Set("Authorization", "Bearer "+loginPayload.Token)
	folderResp, err := ts.Client().Do(folderReq)
	if err != nil {
		t.Fatalf("GET /api/v1/mailbox/folders: %v", err)
	}
	_ = folderResp.Body.Close()
	// Slice-1.5 contract: the read-only listings route through the live
	// IMAP dial and answer LOUDLY when that dial cannot connect — the
	// fixture pins the IMAP endpoint unreachable, so the expected status is
	// the transport 502 IMAP_UNAVAILABLE, never the silent empty list that
	// motivated QA TC-13a and never the gate 503 (the route left the gate).
	if folderResp.StatusCode != http.StatusBadGateway {
		t.Fatalf("GET /api/v1/mailbox/folders = %d, want 502 IMAP_UNAVAILABLE", folderResp.StatusCode)
	}

	msgReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/mailbox/messages", nil)
	msgReq.Header.Set("Authorization", "Bearer "+loginPayload.Token)
	msgResp, err := ts.Client().Do(msgReq)
	if err != nil {
		t.Fatalf("GET /api/v1/mailbox/messages: %v", err)
	}
	_ = msgResp.Body.Close()
	// Same slice-1.5 contract for the message listing: loud IMAP transport
	// failure instead of a gate or a silent empty list.
	if msgResp.StatusCode != http.StatusBadGateway {
		t.Fatalf("GET /api/v1/mailbox/messages = %d, want 502 IMAP_UNAVAILABLE", msgResp.StatusCode)
	}

	sendReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/v1/mailbox/send",
		strings.NewReader(`{"to":["bob@b.example.test"],"text":"hi"}`))
	sendReq.Header.Set("Content-Type", "application/json")
	sendReq.Header.Set("Authorization", "Bearer "+loginPayload.Token)
	sendResp, err := ts.Client().Do(sendReq)
	if err != nil {
		t.Fatalf("POST /api/v1/mailbox/send: %v", err)
	}
	bodyBytes, readErr := io.ReadAll(sendResp.Body)
	if readErr != nil {
		t.Fatalf("read send body: %v", readErr)
	}
	_ = sendResp.Body.Close()
	t.Logf("send response status=%d body=%s", sendResp.StatusCode, string(bodyBytes))
	// slice-3 approves send, so the slice-1 switch no longer rejects it.
	// This fixture runs without a PlatformPool, which degrades the whole
	// mailboxAuthed group to WEBMAIL_DISABLED before the switch — a
	// deployment-wiring state, not the FEATURE_UNAVAILABLE gate. Assert the
	// error code so the two 503s cannot be confused.
	if !strings.Contains(string(bodyBytes), "WEBMAIL_DISABLED") {
		t.Fatalf("send = %d %s, want the fixture WEBMAIL_DISABLED degradation", sendResp.StatusCode, string(bodyBytes))
	}

	readyResp, err := func() (*http.Response, error) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/internal/status/ready", nil)
		return ts.Client().Do(req)
	}()
	if err != nil {
		t.Fatalf("GET /api/v1/internal/status/ready: %v", err)
	}
	_ = readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("PG-only /api/v1/internal/status/ready = %d, want 503", readyResp.StatusCode)
	}
}

func TestDaemonSQLiteStartupFailsClosedOnBadSchemaOrMixedConfig(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	t.Run("missing_sqlite_file_not_created", func(t *testing.T) {
		missing := filepath.Join(dir, "nonexistent.sqlite")
		cfg := &config.Config{
			ListenAddr: "127.0.0.1:8080",
			DevMode:    true,
			DBEngine:   "sqlite",
			SQLitePath: missing,
		}
		if _, _, err := prepareDaemon(ctx, cfg, zap.NewNop()); err == nil {
			t.Fatal("prepareDaemon accepted missing sqlite file")
		}
		if _, err := os.Lstat(missing); !os.IsNotExist(err) {
			t.Fatalf("prepareDaemon created missing sqlite file: err=%v", err)
		}
	})

	t.Run("unmigrated_user_version_0", func(t *testing.T) {
		unmigrated := filepath.Join(dir, "v0.sqlite")
		db, err := sqlitedb.Open(ctx, unmigrated)
		if err != nil {
			t.Fatal(err)
		}
		_ = db.Close()

		cfg := &config.Config{
			ListenAddr: "127.0.0.1:8080",
			DevMode:    true,
			DBEngine:   "sqlite",
			SQLitePath: unmigrated,
		}
		if _, _, err := prepareDaemon(ctx, cfg, zap.NewNop()); err == nil || !strings.Contains(err.Error(), "user_version = 0") {
			t.Fatalf("expected user_version=0 failure, got %v", err)
		}
	})

	t.Run("version_gap_user_version_1", func(t *testing.T) {
		gap := filepath.Join(dir, "v1.sqlite")
		db, err := sqlitedb.Open(ctx, gap)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()

		cfg := &config.Config{
			ListenAddr: "127.0.0.1:8080",
			DevMode:    true,
			DBEngine:   "sqlite",
			SQLitePath: gap,
		}
		if _, _, err := prepareDaemon(ctx, cfg, zap.NewNop()); err == nil || !strings.Contains(err.Error(), "user_version = 1") {
			t.Fatalf("expected user_version=1 failure, got %v", err)
		}
	})

	t.Run("reject_mixed_pg_url", func(t *testing.T) {
		valid := filepath.Join(dir, "valid-mixed.sqlite")
		seedMigratedSQLiteFixture(t, valid)
		cfg := &config.Config{
			ListenAddr:          "127.0.0.1:8080",
			DevMode:             true,
			DBEngine:            "sqlite",
			SQLitePath:          valid,
			DatabaseURLPlatform: "postgres://platform@localhost/urmail",
		}
		if _, _, err := prepareDaemon(ctx, cfg, zap.NewNop()); err == nil || !strings.Contains(err.Error(), "single-engine") {
			t.Fatalf("expected mixed PG rejection, got %v", err)
		}
	})

	t.Run("reject_missing_sqlite_path", func(t *testing.T) {
		cfg := &config.Config{
			ListenAddr: "127.0.0.1:8080",
			DevMode:    true,
			DBEngine:   "sqlite",
			SQLitePath: "",
		}
		if _, _, err := prepareDaemon(ctx, cfg, zap.NewNop()); err == nil || !strings.Contains(err.Error(), "URMAIL_SQLITE_PATH") {
			t.Fatalf("expected missing SQLitePath rejection, got %v", err)
		}
	})
}

func TestDaemonSQLiteDefaultIsolationHookActive(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "urmail-hook.sqlite")
	seedMigratedSQLiteFixture(t, dbPath)

	masterKey := filepath.Join(dir, "master.key")
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(masterKey, keyBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ListenAddr:    "127.0.0.1:0",
		DBEngine:      "sqlite",
		SQLitePath:    dbPath,
		MasterKeyPath: masterKey,
		MyHostname:    "mail.a.example.test",
		WebmailNative: true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, cleanup, err := prepareDaemon(ctx, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("prepareDaemon: %v", err)
	}
	defer cleanup()

	sqliteDB, err := sqlitedb.OpenExisting(ctx, dbPath, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		t.Fatalf("OpenExisting: %v", err)
	}
	defer func() { _ = sqliteDB.Close() }()

	pool, err := db.NewSQLitePool(sqliteDB, isolation.ScopeTenant)
	if err != nil {
		t.Fatalf("NewSQLitePool: %v", err)
	}

	queries := db.Queries(pool)
	querier := db.Querier(pool)

	// Negative: unscoped & wrong-scope & cross-tenant calls fail closed by default
	// after prepareDaemon without any test-only hook registration.
	if _, err := queries.GetMailboxByID(ctx, store.GetMailboxByIDParams{TenantID: 7, ID: 11}); !errors.Is(err, isolation.ErrMissingTenantScope) {
		t.Fatalf("unscoped db.Queries.GetMailboxByID err=%v, want ErrMissingTenantScope", err)
	}
	if _, err := querier.GetMailboxByID(ctx, store.GetMailboxByIDParams{TenantID: 7, ID: 11}); !errors.Is(err, isolation.ErrMissingTenantScope) {
		t.Fatalf("unscoped db.Querier.GetMailboxByID err=%v, want ErrMissingTenantScope", err)
	}
	tenant7Ctx := isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: 7, ActorID: 1})
	platformCtx := isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopePlatform, ActorID: 1})
	if _, err := querier.GetMailboxByID(tenant7Ctx, store.GetMailboxByIDParams{TenantID: 8, ID: 11}); !errors.Is(err, isolation.ErrCrossTenantAccess) {
		t.Fatalf("cross-tenant db.Querier.GetMailboxByID err=%v, want ErrCrossTenantAccess", err)
	}
	if _, err := querier.GetSessionByID(tenant7Ctx, "s"); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("tenant ctx calling GetSessionByID err=%v, want ErrPlatformScopeRequired", err)
	}
	if _, err := queries.GetSessionByID(tenant7Ctx, "s"); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("tenant ctx calling queries.GetSessionByID err=%v, want ErrPlatformScopeRequired", err)
	}

	// WithTx transaction boundary inherits DBTXScopeHook installed by prepareDaemon.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("pool.Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := queries.WithTx(tx)
	if _, err := qtx.GetMailboxByID(platformCtx, store.GetMailboxByIDParams{TenantID: 7, ID: 11}); !errors.Is(err, isolation.ErrMissingTenantScope) {
		t.Fatalf("platform ctx calling WithTx tenant query err=%v, want ErrMissingTenantScope", err)
	}

	// Positive: matching scope passes cleanly on daemon-verified SQLite DB.
	mb, err := qtx.GetMailboxByID(tenant7Ctx, store.GetMailboxByIDParams{TenantID: 7, ID: 11})
	if err != nil || mb.Address != "alice@a.example.test" {
		t.Fatalf("valid WithTx tenant query failed: mb=%+v err=%v", mb, err)
	}
	mb2, err := querier.GetMailboxByID(tenant7Ctx, store.GetMailboxByIDParams{TenantID: 7, ID: 11})
	if err != nil || mb2.ID != 11 {
		t.Fatalf("valid ScopedQuerier query failed: mb=%+v err=%v", mb2, err)
	}
}
