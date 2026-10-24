package middleware_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/handlers"
)

func TestMailboxPrincipalPersistenceScopeAndRevocation(t *testing.T) {
	app, p, tid, uid := transactionV3Fixture(t)
	ctx := context.Background()
	_ = uid
	var domain, mailbox int64
	if err := p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,mode,status,verification_token,verified_at) VALUES($1,'principal.example.test','delegated','active','test',now()) RETURNING id", tid).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash) VALUES($1,$2,'independent','independent@principal.example.test','original-password-hash') RETURNING id", tid, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	key := []byte("01234567890123456789012345678901")
	route := "/api/v1/roundcube/bridge"
	var serial int
	request := func(payload map[string]any, signed bool) *httptest.ResponseRecorder {
		t.Helper()
		serial++
		body, _ := json.Marshal(payload)
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		nonce := fmt.Sprintf("%064x", serial)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, route, strings.NewReader(string(body)))
		if signed {
			mac := hmac.New(sha256.New, key)
			_, _ = mac.Write([]byte("POST\n" + route + "\n" + stamp + "\n" + nonce + "\n"))
			_, _ = mac.Write(body)
			req.Header.Set("X-Urmail-Timestamp", stamp)
			req.Header.Set("X-Urmail-Nonce", nonce)
			req.Header.Set("X-Urmail-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
		// A fresh handler simulates process restarts: authority comes only from PG.
		h := &handlers.MailboxBridgeHandler{Pool: p, Key: key}
		engine := gin.New()
		engine.POST(route, h.Bridge)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}
	login := request(map[string]any{"action": "login", "address": "independent@principal.example.test"}, false)
	if login.Code != 401 {
		t.Fatalf("panel-like unsigned login %d", login.Code)
	}
	mint := func() string {
		t.Helper()
		rec := request(map[string]any{"action": "login", "address": "independent@principal.example.test"}, true)
		var got struct {
			Session string `json:"session"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Session == "" {
			t.Fatalf("login %d %s", rec.Code, rec.Body)
		}
		return got.Session
	}
	session := mint()
	serial--
	if got := request(map[string]any{"action": "login", "address": "independent@principal.example.test"}, true); got.Code != 409 {
		t.Fatalf("signed nonce replay %d", got.Code)
	}
	ticket := request(map[string]any{"action": "authorize", "session": session, "scope": "ai_context", "folder": "INBOX", "uid": 10, "uid_validity": 20}, true)
	var grant struct {
		Ticket string `json:"ticket"`
	}
	_ = json.Unmarshal(ticket.Body.Bytes(), &grant)
	if ticket.Code != 200 || grant.Ticket == "" {
		t.Fatalf("ticket %d %s", ticket.Code, ticket.Body)
	}
	consume := func(ref int) *httptest.ResponseRecorder {
		return request(map[string]any{"action": "consume", "ticket": grant.Ticket, "scope": "ai_context", "folder": "INBOX", "uid": ref, "uid_validity": 20}, true)
	}
	if got := consume(11); got.Code != 403 {
		t.Fatalf("UID tamper %d %s", got.Code, got.Body)
	}
	if got := consume(10); got.Code != 200 || !strings.Contains(got.Body.String(), `"mailbox_id":`+strconv.FormatInt(mailbox, 10)) {
		t.Fatalf("independent principal %d %s", got.Code, got.Body)
	}
	if got := consume(10); got.Code != 403 {
		t.Fatalf("replay %d", got.Code)
	}
	if _, err := p.Exec(ctx, "UPDATE mailboxes SET password_hash='changed' WHERE tenant_id=$1 AND id=$2", tid, mailbox); err != nil {
		t.Fatal(err)
	}
	check := func(token string) int {
		return request(map[string]any{"action": "authorize", "session": token, "scope": "sieve"}, true).Code
	}
	if check(session) != 403 {
		t.Fatal("password change did not revoke persistent session")
	}
	session = mint()
	if _, err := p.Exec(ctx, "UPDATE mailboxes SET status='disabled' WHERE tenant_id=$1 AND id=$2", tid, mailbox); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "UPDATE mailboxes SET status='active' WHERE tenant_id=$1 AND id=$2", tid, mailbox); err != nil {
		t.Fatal(err)
	}
	if check(session) != 403 {
		t.Fatal("disable/enable revived prior session")
	}
	session = mint()
	if got := request(map[string]any{"action": "logout", "session": session}, true); got.Code != 200 {
		t.Fatalf("logout %d", got.Code)
	}
	if check(session) != 403 {
		t.Fatal("logout did not persist")
	}
	if _, err := app.Exec(ctx, "SELECT * FROM mailbox_sessions"); err == nil {
		t.Fatal("panel app can read mailbox service sessions")
	}
	var users int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM users WHERE email='independent@principal.example.test'").Scan(&users); err != nil || users != 0 {
		t.Fatal("independent mailbox was mapped into panel users")
	}
}
