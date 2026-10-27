package carddav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// pgSessionLookup resolves mailbox sessions through the platform store, the
// same path handlers.StoreMailboxNative uses in production.
type pgSessionLookup struct{ q *store.Queries }

func (l pgSessionLookup) GetMailboxSession(ctx context.Context, tokenHash string) (webmail.Session, error) {
	row, err := l.q.PlatformGetMailboxSession(ctx, tokenHash)
	if err != nil {
		return webmail.Session{}, err
	}
	return webmail.Session{
		TokenHash:       row.TokenHash,
		TenantID:        row.TenantID,
		MailboxID:       row.MailboxID,
		Address:         row.Address,
		PasswordVersion: row.PasswordVersion,
		ExpiresAt:       row.ExpiresAt.Time,
	}, nil
}

// carddavFixture wires a migrated database, two mailboxes in one tenant and
// one live mailbox session for mailbox A.
type carddavFixture struct {
	pool     *db.Pool
	queries  *store.Queries
	tenant   int64
	mailboxA int64
	mailboxB int64
	token    string
	srv      *httptest.Server
}

func newCarddavFixture(t *testing.T) *carddavFixture {
	t.Helper()
	ctx := context.Background()
	platform, _ := testdb.OpenMigrated(t)
	q := store.New(platform)
	f := &carddavFixture{pool: platform, queries: q}
	var brand, domain int64
	var err error
	if err = platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('cdv','CardDAV') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'C','cdv-s','active') RETURNING id", brand).Scan(&f.tenant); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'cdv.test','active','tok',now()) RETURNING id", f.tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@cdv.test','x','active') RETURNING id", f.tenant, domain).Scan(&f.mailboxA); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'b','b@cdv.test','x','active') RETURNING id", f.tenant, domain).Scan(&f.mailboxB); err != nil {
		t.Fatal(err)
	}
	f.token = "integration-token-1"
	sum := mailboxprincipal.HashToken(f.token)
	if _, err = q.PlatformCreateMailboxSession(ctx, store.PlatformCreateMailboxSessionParams{TokenHash: sum, Address: "a@cdv.test"}); err != nil {
		t.Fatalf("create mailbox session: %v", err)
	}
	handler := &Handler{
		Service:  appcollab.Service{Q: q},
		Sessions: pgSessionLookup{q},
		Prefix:   "/api/v1/carddav",
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/carddav/", handler)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *carddavFixture) do(t *testing.T, method, path, body string, headers map[string]string, withAuth bool) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestIntegrationCardDAVTwoWayConsistency(t *testing.T) {
	f := newCarddavFixture(t)
	ctx := context.Background()
	// PUT through CardDAV must be visible through the collab/web path.
	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Grace Hopper\r\nEMAIL;TYPE=INTERNET:grace@cdv.test\r\nEND:VCARD\r\n"
	resp := f.do(t, http.MethodPut, "/api/v1/carddav/"+itoa(f.mailboxA)+"/grace.vcf", card, nil, true) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d, want 201", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	rows, err := f.queries.PlatformListContacts(ctx, store.PlatformListContactsParams{TenantID: f.tenant, MailboxID: f.mailboxA, PageLimit: 100})
	if err != nil || len(rows) != 1 || rows[0].DisplayName != "Grace Hopper" {
		t.Fatalf("web list = %+v err %v", rows, err)
	}
	// Create through the collab service (web API path) and read via CardDAV.
	p := appcollab.Principal{TenantID: f.tenant, MailboxID: f.mailboxA}
	book, err := appcollab.Service{Q: f.queries}.EnsureAddressbook(ctx, p, "Contacts")
	if err != nil {
		t.Fatal(err)
	}
	row, err := appcollab.Service{Q: f.queries}.CreateContact(ctx, p, book.ID, collab.Contact{
		DisplayName: "Alan Turing", Emails: []collab.Email{{Address: "alan@cdv.test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp = f.do(t, http.MethodGet, "/api/v1/carddav/"+itoa(f.mailboxA)+"/"+itoa(row.ID)+".vcf", "", nil, true) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET web-created contact = %d", resp.StatusCode)
	}
	body := readAll(t, resp)
	if !strings.Contains(body, "FN:Alan Turing") || !strings.Contains(body, "UID:"+itoa(row.ID)+"@urmail") {
		t.Fatalf("card = %q", body)
	}
	_ = location
}

func TestIntegrationSyncCollectionDeltaAndTombstone(t *testing.T) {
	f := newCarddavFixture(t)
	ctx := context.Background()
	svc := appcollab.Service{Q: f.queries}
	p := appcollab.Principal{TenantID: f.tenant, MailboxID: f.mailboxA}
	book, err := svc.EnsureAddressbook(ctx, p, "Contacts")
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.CreateContact(ctx, p, book.ID, collab.Contact{DisplayName: "First"})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/carddav/" + itoa(f.mailboxA) + "/"
	syncBody := func(token string) string {
		return "<D:sync-collection xmlns:D=\"DAV:\"><D:sync-token>" + token + "</D:sync-token><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>"
	}
	// Initial sync establishes the baseline token.
	resp := f.do(t, "REPORT", base, syncBody(""), nil, true) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	initial := readAll(t, resp)
	token := tokenFrom(initial)
	if token == "" {
		t.Fatalf("initial sync missing token: %s", initial)
	}
	// Web-path update and delete must be the only delta afterwards.
	updated, err := svc.UpdateContact(ctx, p, first.ID, first.Version, collab.Contact{DisplayName: "First Prime"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Q.PlatformDeleteContact(ctx, store.PlatformDeleteContactParams{
		TenantID: f.tenant, MailboxID: f.mailboxA, ID: first.ID, Version: updated.Version,
	}); err != nil {
		t.Fatal(err)
	}
	resp = f.do(t, "REPORT", base, syncBody(token), nil, true) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	delta := readAll(t, resp)
	if strings.Count(delta, "<D:response>") != 1 {
		t.Fatalf("delta should contain exactly the deleted contact: %s", delta)
	}
	if !strings.Contains(delta, "404 Not Found") {
		t.Fatalf("tombstone must be a 404 change: %s", delta)
	}
	newToken := tokenFrom(delta)
	// Re-sync from the same baseline must not resurrect the contact.
	resp = f.do(t, "REPORT", base, syncBody(token), nil, true) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	again := readAll(t, resp)
	if strings.Count(again, "<D:response>") != 1 || !strings.Contains(again, "404 Not Found") {
		t.Fatalf("baseline re-sync should show the same tombstone: %s", again)
	}
	// From the new token the tombstone must not appear again.
	resp = f.do(t, "REPORT", base, syncBody(newToken), nil, true) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	clean := readAll(t, resp)
	if strings.Contains(clean, "<D:response>") {
		t.Fatalf("no changes expected after tombstone consumed: %s", clean)
	}
	// The contact stays deleted.
	if _, err = f.queries.PlatformGetContact(ctx, store.PlatformGetContactParams{
		TenantID: f.tenant, MailboxID: f.mailboxA, ID: first.ID,
	}); err == nil {
		t.Fatal("deleted contact resurrected")
	}
}

func TestIntegrationIfMatchConflict412(t *testing.T) {
	f := newCarddavFixture(t)
	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Conflict\r\nEND:VCARD\r\n"
	resp := f.do(t, http.MethodPut, "/api/v1/carddav/"+itoa(f.mailboxA)+"/c.vcf", card, nil, true) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	resp = f.do(t, http.MethodPut, location, card, map[string]string{"If-Match": "\"42\""}, true) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match = %d, want 412", resp.StatusCode)
	}
	// The correct ETag updates cleanly.
	resp = f.do(t, http.MethodPut, location, card, map[string]string{"If-Match": "\"1\""}, true) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("fresh If-Match = %d, want 204", resp.StatusCode)
	}
}

func TestIntegrationAnonymousAndCrossMailboxRejected(t *testing.T) {
	f := newCarddavFixture(t)
	base := "/api/v1/carddav/" + itoa(f.mailboxA) + "/"
	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:X\r\nEND:VCARD\r\n"
	resp := f.do(t, http.MethodPut, base+"x.vcf", card, nil, false) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", resp.StatusCode)
	}
	// Authenticated as mailbox A, addressing mailbox B must fail closed.
	other := "/api/v1/carddav/" + itoa(f.mailboxB) + "/"
	resp = f.do(t, "PROPFIND", other, "", nil, true) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-mailbox PROPFIND = %d, want 403", resp.StatusCode)
	}
	resp = f.do(t, http.MethodPut, other+"y.vcf", card, nil, true) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-mailbox PUT = %d, want 403", resp.StatusCode)
	}
	resp = f.do(t, "REPORT", other, "<D:sync-collection xmlns:D=\"DAV:\"><D:sync-token/></D:sync-collection>", nil, true) //nolint:bodyclose // closed via t.Cleanup in carddavFixture.do
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-mailbox REPORT = %d, want 403", resp.StatusCode)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
