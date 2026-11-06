package middleware_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/store"
)

type sieveActivation struct {
	tenant, mailbox int64
	calls           int
}

func (s *sieveActivation) ActivateSieve(_ context.Context, tenant, mailbox int64, _ string) error {
	s.tenant, s.mailbox = tenant, mailbox
	s.calls++
	return nil
}

func TestSieveBridgeIsOneScopeIndependentMailboxIdentity(t *testing.T) {
	app, p, tid, _ := transactionV3Fixture(t)
	ctx := context.Background()
	var domain, mid int64
	if err := p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'sieve.example.test','active','test',now()) RETURNING id", tid).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash) VALUES($1,$2,'own','own@sieve.example.test','test') RETURNING id", tid, domain).Scan(&mid); err != nil {
		t.Fatal(err)
	}
	q := store.New(p)
	_, sessionHash, _ := mailboxprincipal.NewToken()
	if _, err := q.PlatformCreateMailboxSession(ctx, store.PlatformCreateMailboxSessionParams{TokenHash: sessionHash, Address: "own@sieve.example.test"}); err != nil {
		t.Fatal(err)
	}
	ticket, ticketHash, _ := mailboxprincipal.NewToken()
	if _, err := q.PlatformCreateMailboxTicket(ctx, store.PlatformCreateMailboxTicketParams{TokenHash: ticketHash, SessionHash: sessionHash, Scope: "sieve"}); err != nil {
		t.Fatal(err)
	}
	activation := &sieveActivation{}
	h := &handlers.SieveBridgeHandler{Pool: p, Key: []byte("01234567890123456789012345678901"), Activator: activation}
	r := gin.New()
	r.POST("/api/v1/mailbox/sieve/exchange", h.Exchange)
	r.GET("/api/v1/mailbox/sieve/session", h.Session)
	r.PUT("/api/v1/mailbox/sieve/rules", h.Save)
	exchange := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://panel.example.test/api/v1/mailbox/sieve/exchange", strings.NewReader(url.Values{"ticket": {ticket}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	if got := exchange("https://attacker.example.test"); got.Code != 403 {
		t.Fatalf("cross-origin exchange %d", got.Code)
	}
	rec := exchange("https://panel.example.test")
	if rec.Code != 303 || strings.Contains(rec.Header().Get("Location"), ticket) {
		t.Fatalf("exchange leaked or failed: %d %s", rec.Code, rec.Body)
	}
	var cookie *http.Cookie
	for _, candidate := range rec.Result().Cookies() {
		if candidate.Name == "urmail_mailbox_sieve" {
			cookie = candidate
		}
	}
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/api/v1/mailbox/sieve" {
		t.Fatalf("unscoped bridge cookie %+v", cookie)
	}
	if got := exchange("https://panel.example.test"); got.Code != 403 {
		t.Fatalf("bridge ticket replay %d", got.Code)
	}
	request := func(method, path, body, csrf string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, "https://panel.example.test"+path, strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Urmail-Sieve-CSRF", csrf)
		got := httptest.NewRecorder()
		r.ServeHTTP(got, req)
		return got
	}
	got := request(http.MethodGet, "/api/v1/mailbox/sieve/session", "", "")
	var session struct {
		Mailbox string `json:"mailbox"`
		CSRF    string `json:"csrf"`
		Version string `json:"version"`
	}
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &session) != nil || session.Mailbox != "own@sieve.example.test" {
		t.Fatalf("own mailbox view %d %s", got.Code, got.Body)
	}
	body := `{"expected_version":"` + session.Version + `","mailbox_id":999,"tenant_id":999,"confirmed":true,"rules":{"rules":[{"header":"subject","match":"contains","value":"test","action":"keep"}]}}`
	if got = request(http.MethodPut, "/api/v1/mailbox/sieve/rules", body, ""); got.Code != 403 {
		t.Fatalf("CSRF bypass %d", got.Code)
	}
	if got = request(http.MethodPut, "/api/v1/mailbox/sieve/rules", body, session.CSRF); got.Code != 200 || activation.calls != 1 || activation.tenant != tid || activation.mailbox != mid {
		t.Fatalf("mailbox selector escaped ticket %d %s %+v", got.Code, got.Body, activation)
	}
	if stale := request(http.MethodPut, "/api/v1/mailbox/sieve/rules", body, session.CSRF); stale.Code != 409 || activation.calls != 1 {
		t.Fatalf("stale editor overwrote newer rules: %d", stale.Code)
	}
	current := request(http.MethodGet, "/api/v1/mailbox/sieve/session", "", "")
	if json.Unmarshal(current.Body.Bytes(), &session) != nil {
		t.Fatal("missing current rules version")
	}
	concurrentBody := body
	var staleBody map[string]any
	if json.Unmarshal([]byte(concurrentBody), &staleBody) != nil {
		t.Fatal("bad fixture")
	}
	staleBody["expected_version"] = session.Version
	encoded, _ := json.Marshal(staleBody)
	statuses := make(chan int, 2)
	for _, value := range []string{"left", "right"} {
		go func(value string) {
			proposal := strings.Replace(string(encoded), "\"value\":\"test\"", "\"value\":\""+value+"\"", 1)
			statuses <- request(http.MethodPut, "/api/v1/mailbox/sieve/rules", proposal, session.CSRF).Code
		}(value)
	}
	first, second := <-statuses, <-statuses
	counts := map[int]int{}
	counts[first]++
	counts[second]++
	if counts[200] != 1 || counts[409] != 1 || activation.calls != 2 {
		t.Fatalf("concurrent editors lost an update: %d %d calls=%d", first, second, activation.calls)
	}
	panel := api.New(api.Options{AppPool: app, PlatformPool: p, DevMode: true, Logger: zap.NewNop()})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/platform/overview", nil)
	req.AddCookie(cookie)
	denied := httptest.NewRecorder()
	panel.ServeHTTP(denied, req)
	if denied.Code != 401 {
		t.Fatalf("Sieve bridge became panel session: %d", denied.Code)
	}
	// A legacy mailbox remains owned by ManageSieve, even if obsolete desired
	// metadata happens to contain a script the structured decoder cannot parse.
	rawLegacy := "require [\"variables\"]; set \"source_rule\" \"preserved\"; keep;\n"
	// Test-only seed of a preexisting backend in this owned disposable database.
	// The guard is restored within the same private transaction before requests.
	fixtureTx, fixtureErr := p.Begin(ctx)
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	defer func() { _ = fixtureTx.Rollback(context.Background()) }()
	if _, fixtureErr = fixtureTx.Exec(ctx, "ALTER TABLE mailboxes DISABLE TRIGGER mailbox_sieve_mode_guard"); fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if _, fixtureErr = fixtureTx.Exec(ctx, "UPDATE mailboxes SET sieve_mode='legacy_v80',sieve_script=$3 WHERE tenant_id=$1 AND id=$2", tid, mid, rawLegacy); fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if _, fixtureErr = fixtureTx.Exec(ctx, "ALTER TABLE mailboxes ENABLE TRIGGER mailbox_sieve_mode_guard"); fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if fixtureErr = fixtureTx.Commit(ctx); fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	legacy := request(http.MethodGet, "/api/v1/mailbox/sieve/session", "", "")
	if legacy.Code != 200 || !strings.Contains(legacy.Body.String(), "legacy_managesieve") || strings.Contains(legacy.Body.String(), "source_rule") {
		t.Fatalf("legacy script decoded or exposed: %d %s", legacy.Code, legacy.Body)
	}
	if denied := request(http.MethodPut, "/api/v1/mailbox/sieve/rules", string(encoded), session.CSRF); denied.Code != 409 || !strings.Contains(denied.Body.String(), "SIEVE_LEGACY_MANAGESIEVE_REQUIRED") || activation.calls != 2 {
		t.Fatalf("legacy script overwritten: %d %s calls=%d", denied.Code, denied.Body, activation.calls)
	}
	var preserved string
	if err := p.QueryRow(ctx, "SELECT sieve_script FROM mailboxes WHERE tenant_id=$1 AND id=$2", tid, mid).Scan(&preserved); err != nil || preserved != rawLegacy {
		t.Fatalf("legacy metadata changed: %v", err)
	}
	if err := q.PlatformRevokeMailboxSession(ctx, sessionHash); err != nil {
		t.Fatal(err)
	}
	if got = request(http.MethodGet, "/api/v1/mailbox/sieve/session", "", ""); got.Code != 403 {
		t.Fatalf("parent logout did not invalidate bridge: %d", got.Code)
	}
}
