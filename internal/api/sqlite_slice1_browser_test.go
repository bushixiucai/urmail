package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
)

// TestSlice1SQLiteBrowserSession drives the frozen real API composition through
// Chromium and the local Vite proxy. It shares newSlice1Server's real migrated
// temporary SQLite database and Alice fixture; only the session-expiry control
// endpoint is test-local.
func TestSlice1SQLiteBrowserSession(t *testing.T) {
	db, apiServer := newSlice1Server(t)
	ctx := context.Background()
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/expire-session":
			var body struct {
				Token string `json:"token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
				http.Error(w, "missing token", http.StatusBadRequest)
				return
			}
			result, err := db.ExecContext(ctx,
				`UPDATE mailbox_sessions SET expires_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)-1000 WHERE token_hash=?`,
				mailboxprincipal.HashToken(body.Token),
			)
			if err != nil {
				http.Error(w, "expire session", http.StatusInternalServerError)
				return
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				http.Error(w, fmt.Sprintf("expired rows=%d err=%v", changed, err), http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"expired":true}`))
		case "/seed-inbox":
			for _, stmt := range []string{
				`CREATE TABLE IF NOT EXISTS mailbox_folders (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					tenant_id INTEGER NOT NULL,
					mailbox_id INTEGER NOT NULL,
					name TEXT NOT NULL,
					delimiter TEXT NOT NULL DEFAULT '/',
					flags_json TEXT NOT NULL DEFAULT '[]'
				)`,
				`CREATE TABLE IF NOT EXISTS mailbox_messages (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					tenant_id INTEGER NOT NULL,
					mailbox_id INTEGER NOT NULL,
					folder TEXT NOT NULL,
					uid_validity INTEGER NOT NULL DEFAULT 1,
					uid INTEGER NOT NULL,
					subject TEXT NOT NULL,
					from_name TEXT NOT NULL DEFAULT '',
					from_email TEXT NOT NULL DEFAULT '',
					date_micros INTEGER NOT NULL DEFAULT 0,
					flags_json TEXT NOT NULL DEFAULT '[]',
					size_bytes INTEGER NOT NULL DEFAULT 0,
					has_attachments INTEGER NOT NULL DEFAULT 0,
					preview TEXT NOT NULL DEFAULT '',
					message_id TEXT NOT NULL DEFAULT '',
					in_reply_to TEXT NOT NULL DEFAULT ''
				)`,
				`DELETE FROM mailbox_folders WHERE tenant_id=7 AND mailbox_id=11`,
				`DELETE FROM mailbox_messages WHERE tenant_id=7 AND mailbox_id=11`,
				`INSERT INTO mailbox_folders(tenant_id,mailbox_id,name,delimiter,flags_json) VALUES
					(7,11,'INBOX','/','["\\HasNoChildren"]'),
					(7,11,'Sent','/','["\\HasNoChildren","\\Sent"]'),
					(7,11,'Drafts','/','["\\HasNoChildren","\\Drafts"]'),
					(7,11,'Trash','/','["\\HasNoChildren","\\Trash"]'),
					(7,11,'Archive','/','["\\HasNoChildren","\\Archive"]')`,
				`INSERT INTO mailbox_messages(tenant_id,mailbox_id,folder,uid_validity,uid,subject,from_name,from_email,date_micros,flags_json,size_bytes,has_attachments,preview,message_id) VALUES
					(7,11,'INBOX',42,101,'Alice Welcome to the Mobile Inbox Layout Verification','System Notification','sys@a.example.test',1737412800000000,'["\\Seen"]',512,0,'Welcome alice to your read-only SQLite inbox view with long preview text','<m1@a.example.test>'),
					(7,11,'INBOX',42,102,'Alice Urgent Contract Review & Attachment Notice','Partner Operations','partner@a.example.test',1737412900000000,'[]',1024,1,'Please review the updated terms and verify mobile list truncation','<m2@a.example.test>')`,
			} {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					http.Error(w, fmt.Sprintf("seed inbox %q: %v", stmt, err), http.StatusInternalServerError)
					return
				}
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"seeded":2}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer control.Close()

	webDir := filepath.Join("..", "..", "web")
	vitePort := freeTCPPort(t)
	viteURL := "http://127.0.0.1:" + strconv.Itoa(vitePort)
	vite := exec.Command("./node_modules/.bin/vite", "--host", "127.0.0.1", "--port", strconv.Itoa(vitePort), "--strictPort")
	vite.Dir = webDir
	vite.Env = append(os.Environ(), "URMAIL_DEV_API="+apiServer.URL, "URMAIL_E2E_NATIVE_FIXTURE=1")
	vite.Stdout, vite.Stderr = os.Stdout, os.Stderr
	if err := vite.Start(); err != nil {
		t.Fatalf("start local Vite server: %v", err)
	}
	viteDone := make(chan error, 1)
	go func() { viteDone <- vite.Wait() }()
	defer func() {
		if vite.Process != nil {
			_ = vite.Process.Kill()
			select {
			case <-viteDone:
			case <-time.After(5 * time.Second):
				t.Errorf("Vite process did not exit after kill")
			}
		}
	}()
	waitForLoopback(t, viteURL, viteDone)

	spec := os.Getenv("URMAIL_E2E_SQLITE_SPEC")
	if spec == "" {
		spec = "e2e/sqlite-session-browser.spec.ts"
	}
	playwright := exec.Command("./node_modules/.bin/playwright", "test", spec, "--workers=1")
	playwright.Dir = webDir
	playwright.Env = append(os.Environ(),
		"URMAIL_E2E_SQLITE_BROWSER=1",
		"URMAIL_E2E_SQLITE_SPEC="+spec,
		"URMAIL_E2E_BASE_URL="+viteURL,
		"URMAIL_E2E_SQLITE_CONTROL_URL="+control.URL,
		"PLAYWRIGHT_BROWSERS_PATH=/var/lib/openbot/.cache/ms-playwright",
	)
	playwright.Stdout, playwright.Stderr = os.Stdout, os.Stderr
	playwrightErr := playwright.Run()

	if spec == "e2e/sqlite-mobile-login.spec.ts" {
		if playwrightErr != nil {
			t.Fatalf("Playwright mobile inbox command failed: %v", playwrightErr)
		}
		var sessions int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM mailbox_sessions WHERE tenant_id=7 AND mailbox_id=11 AND revoked_at IS NULL`,
		).Scan(&sessions); err != nil || sessions < 1 {
			t.Fatalf("SQLite browser login sessions=%d err=%v, want at least one active session", sessions, err)
		}
		var seededMessages int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM mailbox_messages WHERE tenant_id=7 AND mailbox_id=11 AND folder='INBOX'`,
		).Scan(&seededMessages); err != nil || seededMessages != 2 {
			t.Fatalf("SQLite seeded inbox messages=%d err=%v, want 2", seededMessages, err)
		}
		t.Logf("SQLite evidence: browser login created %d active Alice session rows and rendered %d seeded INBOX rows; Vite PID %d terminated by test cleanup", sessions, seededMessages, vite.Process.Pid)
		return
	}
	if playwrightErr != nil {
		t.Fatalf("Playwright command failed: %v", playwrightErr)
	}

	var displayName, signature string
	var version int64
	if err := db.QueryRowContext(ctx,
		`SELECT display_name,signature_text,version FROM mailbox_settings WHERE tenant_id=7 AND mailbox_id=11`,
	).Scan(&displayName, &signature, &version); err != nil {
		t.Fatalf("read browser-written SQLite settings: %v", err)
	}
	if displayName != "SQLite Browser Alice" || version != 1 {
		t.Fatalf("SQLite settings=(%q,%q,v%d), want browser write and no expired retry", displayName, signature, version)
	}
	t.Logf("SQLite evidence: mailbox_settings display_name=%q version=%d signature_bytes=%d; expired second write left row unchanged", displayName, version, len(signature))
	var expired int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM mailbox_sessions WHERE expires_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000) AND revoked_at IS NULL`,
	).Scan(&expired); err != nil || expired != 1 {
		t.Fatalf("expired session rows=%d err=%v, want exactly one", expired, err)
	}
	t.Logf("SQLite evidence: one mailbox session expired in the temporary database; Vite PID %d terminated by test cleanup", vite.Process.Pid)
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}
	return port
}

func waitForLoopback(t *testing.T, address string, processDone <-chan error) {
	t.Helper()
	client := &http.Client{Timeout: 300 * time.Millisecond}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-processDone:
			t.Fatalf("Vite exited before ready: %v", err)
		default:
		}
		response, err := client.Get(address)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Vite did not become ready at %s", strings.TrimSpace(address))
}
