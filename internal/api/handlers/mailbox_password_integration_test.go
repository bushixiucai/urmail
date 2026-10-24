package handlers

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestMailboxPasswordBridgeOldProofRevocationCASAndAudit(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	q := store.New(s.p)
	oldPassword, newPassword := "Legacy-old-password-123", "Rotated-new-password-456"
	oldHash, err := domainMail.HashPassword(oldPassword)
	if err != nil {
		t.Fatal(err)
	}
	var domainID, mailboxID int64
	var fqdn string
	if err = s.p.QueryRow(ctx, "SELECT id,fqdn FROM domains WHERE tenant_id=$1 ORDER BY id LIMIT 1", s.tid).Scan(&domainID, &fqdn); err != nil {
		t.Fatal(err)
	}
	address := "owner@" + fqdn
	if err = s.p.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'owner',$3,$4,128,'active') RETURNING id", s.tid, domainID, address, oldHash).Scan(&mailboxID); err != nil {
		t.Fatal(err)
	}
	session := func() string {
		t.Helper()
		token, hash, e := mailboxprincipal.NewToken()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = q.PlatformCreateMailboxSession(ctx, store.PlatformCreateMailboxSessionParams{TokenHash: hash, Address: address}); e != nil {
			t.Fatal(e)
		}
		return token
	}
	token, otherSession := session(), session()
	ticketToken, ticketHash, err := mailboxprincipal.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformCreateMailboxTicket(ctx, store.PlatformCreateMailboxTicketParams{TokenHash: ticketHash, Scope: "sieve", SessionHash: mailboxprincipal.HashToken(token)}); err != nil {
		t.Fatal(err)
	}
	key := []byte(strings.Repeat("k", 32))
	handler := &MailboxBridgeHandler{Pool: s.p, Key: key}
	router := gin.New()
	router.POST("/api/v1/roundcube/bridge", handler.Bridge)
	call := func(input mailboxBridgeRequest, signed bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(input)
		request := httptest.NewRequestWithContext(ctx, "POST", "/api/v1/roundcube/bridge", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		if signed {
			nonceBytes := make([]byte, 32)
			if _, e := rand.Read(nonceBytes); e != nil {
				panic(e)
			}
			nonce := hex.EncodeToString(nonceBytes)
			stamp := strconv.FormatInt(time.Now().Unix(), 10)
			mac := hmac.New(sha256.New, key)
			_, _ = fmt.Fprintf(mac, "POST\n/api/v1/roundcube/bridge\n%s\n%s\n", stamp, nonce)
			_, _ = mac.Write(body)
			request.Header.Set("X-Urmail-Timestamp", stamp)
			request.Header.Set("X-Urmail-Nonce", nonce)
			request.Header.Set("X-Urmail-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	input := mailboxBridgeRequest{Action: "change_password", Scope: oldPassword, Session: token, Address: address, OldPassword: oldPassword, NewPassword: newPassword}
	if w := call(input, false); w.Code != 401 {
		t.Fatalf("panel/unsigned request authorized %d", w.Code)
	}
	wrongAddress := input
	wrongAddress.Address = "another@timeline.test"
	if w := call(wrongAddress, true); w.Code != 403 {
		t.Fatalf("session identity substitution %d %s", w.Code, w.Body)
	}
	wrong := input
	wrong.OldPassword = "incorrect-old-password"
	if w := call(wrong, true); w.Code != 403 {
		t.Fatalf("service login signature bypassed old proof %d %s", w.Code, w.Body)
	}
	// Mandatory successful audit and the password update must share the same transaction.
	if _, err = s.p.Exec(ctx, "CREATE FUNCTION reject_password_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN('mailbox.change_password','mailbox.password.rejected') THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_password_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_password_audit()"); err != nil {
		t.Fatal(err)
	}
	if w := call(wrong, true); w.Code != 503 {
		t.Fatalf("failed rejection audit did not fail closed %d %s", w.Code, w.Body)
	}
	if w := call(input, true); w.Code != 503 {
		t.Fatalf("audit failure did not fail closed %d %s", w.Code, w.Body)
	}
	var hash string
	var version int64
	var pending bool
	read := func() {
		t.Helper()
		if e := s.p.QueryRow(ctx, "SELECT password_hash,password_version,password_revoke_pending FROM mailboxes WHERE id=$1 AND tenant_id=$2", mailboxID, s.tid).Scan(&hash, &version, &pending); e != nil {
			t.Fatal(e)
		}
	}
	read()
	if hash != oldHash || version != 0 || pending {
		t.Fatal("failed audit committed credential mutation")
	}
	if _, err = s.p.Exec(ctx, "DROP TRIGGER reject_password_audit ON audit_logs; DROP FUNCTION reject_password_audit()"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- call(input, true) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for w := range results {
		if w.Code == 200 {
			successes++
			if !strings.Contains(w.Body.String(), `"imap_revocation":"pending"`) {
				t.Fatal("synchronous disconnect falsely claimed")
			}
		} else if w.Code != 403 && w.Code != 409 {
			t.Fatalf("concurrent change %d %s", w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), oldPassword) || strings.Contains(w.Body.String(), newPassword) || strings.Contains(w.Body.String(), oldHash) {
			t.Fatal("credential exposed in response")
		}
	}
	if successes != 1 {
		t.Fatalf("CAS allowed %d winners", successes)
	}
	read()
	if !domainMail.VerifyPassword(newPassword, hash) || domainMail.VerifyPassword(oldPassword, hash) || version != 1 || !pending {
		t.Fatal("password rotation or durable revocation missing")
	}
	for _, stale := range []string{token, otherSession} {
		if _, e := q.PlatformGetMailboxSession(ctx, mailboxprincipal.HashToken(stale)); e == nil {
			t.Fatal("old bridge session survived")
		}
	}
	if _, e := q.PlatformConsumeMailboxTicket(ctx, store.PlatformConsumeMailboxTicketParams{TokenHash: mailboxprincipal.HashToken(ticketToken), Scope: "sieve"}); e == nil {
		t.Fatal("old operation ticket survived")
	}
	var detail string
	if err = s.p.QueryRow(ctx, "SELECT coalesce(string_agg(detail::text,' '),'') FROM audit_logs WHERE tenant_id=$1", s.tid).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{oldPassword, newPassword, oldHash, hash, token, otherSession} {
		if strings.Contains(detail, secret) {
			t.Fatal("audit exposed credential")
		}
	}
	if w := call(input, true); w.Code != 403 {
		t.Fatalf("revoked session replay %d", w.Code)
	}
	fresh := session()
	bad := input
	bad.Session = fresh
	bad.OldPassword = "bad-password"
	bad.NewPassword = "Third-password-789"
	// One earlier old-password rejection is already in the same mailbox's window.
	baseline, err := q.PlatformMailboxPasswordFailures(ctx, store.PlatformMailboxPasswordFailuresParams{TenantID: s.tid, MailboxID: mailboxID})
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 10-baseline; i++ {
		if w := call(bad, true); w.Code != 403 {
			t.Fatalf("failure threshold %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := call(bad, true); w.Code != 429 {
		t.Fatalf("durable mailbox attempt limit absent %d %s", w.Code, w.Body)
	}
}
