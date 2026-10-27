package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestStandardProtocolRoutesAuthenticateAndPersistPG(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	ctx := context.Background()
	password := "protocol-route-fixture-password"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	var tenant, domain, mailbox int64
	if err = platform.QueryRow(ctx, `WITH b AS(INSERT INTO brands(slug,name)VALUES('protocol-routing','Protocol')RETURNING id),t AS(INSERT INTO tenants(brand_id,name,slug,status)SELECT id,'Protocol','protocol-routing','active'FROM b RETURNING id)SELECT id FROM t`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,provisioning_state)VALUES($1,'protocol-route.example.test','active','fixture',now(),'ready')RETURNING id`, tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes)VALUES($1,$2,'client','client@protocol-route.example.test',$3,1048576)RETURNING id`, tenant, domain, hash).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	// Standard protocol clients remain independent of the browser UI switch.
	engine := New(Options{PlatformPool: platform, AppPool: app, WebmailNative: false})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:24101"
		req.SetBasicAuth("client@protocol-route.example.test", password)
		if method == http.MethodPut {
			req.Header.Set("If-None-Match", "*")
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}
	if w := call("PROPFIND", "/caldav/", `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:allprop/></d:propfind>`); w.Code != http.StatusMultiStatus {
		t.Fatalf("CalDAV principal: %d %s", w.Code, w.Body.String())
	}
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//urMail//Protocol test//EN\r\nBEGIN:VEVENT\r\nUID:route-calendar-1\r\nDTSTAMP:20261027T000000Z\r\nDTSTART:20261101T080000Z\r\nDTEND:20261101T090000Z\r\nSUMMARY:Route test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if w := call(http.MethodPut, "/caldav/calendar/route-calendar-1.ics", ics); w.Code != http.StatusCreated {
		t.Fatalf("CalDAV write: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, "/caldav/calendar/route-calendar-1.ics", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Route test") {
		t.Fatalf("CalDAV read: %d %s", w.Code, w.Body.String())
	}
	if w := call("PROPFIND", "/api/v1/carddav/", `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:allprop/></d:propfind>`); w.Code != http.StatusMultiStatus {
		t.Fatalf("CardDAV principal: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodOptions, "/Microsoft-Server-ActiveSync", ""); w.Code != http.StatusOK || w.Header().Get("MS-ASProtocolVersions") == "" {
		t.Fatalf("EAS options: %d %s", w.Code, w.Body.String())
	}
	var liveSessions int
	if err = platform.QueryRow(ctx, `SELECT count(*) FROM mailbox_sessions WHERE mailbox_id=$1 AND revoked_at IS NULL`, mailbox).Scan(&liveSessions); err != nil {
		t.Fatal(err)
	}
	if liveSessions != 0 {
		t.Fatalf("ephemeral protocol sessions retained: %d", liveSessions)
	}
}

func TestDisabledProtocolAliasesAndDiscoveryAreUnavailable(t *testing.T) {
	disabled := false
	engine := New(Options{CardDAVEnabled: &disabled, CalDAVEnabled: &disabled, EASEnabled: &disabled})
	for _, path := range []string{"/.well-known/carddav", "/.well-known/caldav", "/caldav/calendar/", "/api/v1/carddav/", "/api/v1/caldav/calendar/", "/Microsoft-Server-ActiveSync", "/api/v1/eas", "/api/v1/mailbox/eas", "/api/v1/mailbox/carddav/addressbook", "/api/v1/mailbox/caldav/calendar"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("disabled %s status %d", path, w.Code)
		}
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/autodiscover/autodiscover.xml", strings.NewReader(`<?xml version="1.0"?><Autodiscover><Request><EMailAddress>client@example.test</EMailAddress></Request></Autodiscover>`))
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), "Microsoft-Server-ActiveSync") || strings.Contains(w.Body.String(), "<Type>MobileSync</Type>") {
		t.Fatalf("disabled EAS advertised: %s", w.Body.String())
	}
}
