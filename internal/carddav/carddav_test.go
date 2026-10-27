package carddav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// fakeStore is an in-memory collab.Store for protocol tests. It mirrors the
// SQL behavior the adapter relies on: version increments, tombstones in the
// change log and strict tenant/mailbox predicates.
type fakeStore struct {
	tenants        map[int64]int64 // tenant -> mailbox for predicate checks
	contacts       map[int64]store.MailboxContacts
	changes        []store.MailboxChanges
	nextID         int64
	crossTenantErr bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		tenants:  map[int64]int64{7: 42},
		contacts: map[int64]store.MailboxContacts{},
		nextID:   99,
	}
}

func (f *fakeStore) log(tenant, mailbox, resource int64, version int64, deleted bool) {
	f.changes = append(f.changes, store.MailboxChanges{
		ID: int64(len(f.changes) + 1), TenantID: tenant, MailboxID: mailbox,
		ResourceType: "contact", ResourceID: resource, Version: version, Deleted: deleted,
	})
}

func (f *fakeStore) check(tenant, mailbox int64) error {
	if f.crossTenantErr || f.tenants[tenant] != mailbox {
		return pgx.ErrNoRows
	}
	return nil
}

func (f *fakeStore) PlatformEnsureAddressbook(_ context.Context, arg store.PlatformEnsureAddressbookParams) (store.MailboxAddressbooks, error) {
	if err := f.check(arg.TenantID, arg.MailboxID); err != nil {
		return store.MailboxAddressbooks{}, err
	}
	return store.MailboxAddressbooks{ID: 5, TenantID: arg.TenantID, MailboxID: arg.MailboxID, Name: arg.Name, Version: 1}, nil
}

func (f *fakeStore) PlatformGetAddressbookByName(_ context.Context, arg store.PlatformGetAddressbookByNameParams) (store.MailboxAddressbooks, error) {
	return f.PlatformEnsureAddressbook(context.TODO(), store.PlatformEnsureAddressbookParams(arg))
}

func (f *fakeStore) PlatformInsertContact(_ context.Context, arg store.PlatformInsertContactParams) (store.PlatformInsertContactRow, error) {
	if err := f.check(arg.TenantID, arg.MailboxID); err != nil {
		return store.PlatformInsertContactRow{}, err
	}
	f.nextID++
	row := store.MailboxContacts{
		ID: f.nextID, TenantID: arg.TenantID, MailboxID: arg.MailboxID,
		AddressbookID: arg.AddressbookID, Version: 1,
		DisplayName: arg.DisplayName, GivenName: arg.GivenName, FamilyName: arg.FamilyName,
		Emails: arg.Emails, Phones: arg.Phones, Note: arg.Note,
	}
	f.contacts[row.ID] = row
	f.log(arg.TenantID, arg.MailboxID, row.ID, 1, false)
	return store.PlatformInsertContactRow{
		ID: row.ID, TenantID: row.TenantID, MailboxID: row.MailboxID,
		AddressbookID: row.AddressbookID, Version: row.Version,
		DisplayName: row.DisplayName, GivenName: row.GivenName, FamilyName: row.FamilyName,
		Emails: row.Emails, Phones: row.Phones, Note: row.Note,
	}, nil
}

func (f *fakeStore) PlatformUpdateContact(_ context.Context, arg store.PlatformUpdateContactParams) (store.PlatformUpdateContactRow, error) {
	if err := f.check(arg.TenantID, arg.MailboxID); err != nil {
		return store.PlatformUpdateContactRow{}, err
	}
	row, ok := f.contacts[arg.ID]
	if !ok || row.Version != arg.ExpectedVersion {
		return store.PlatformUpdateContactRow{}, pgx.ErrNoRows
	}
	row.Version++
	row.DisplayName, row.GivenName, row.FamilyName = arg.DisplayName, arg.GivenName, arg.FamilyName
	row.Emails, row.Phones, row.Note = arg.Emails, arg.Phones, arg.Note
	f.contacts[row.ID] = row
	f.log(arg.TenantID, arg.MailboxID, row.ID, row.Version, false)
	return store.PlatformUpdateContactRow{
		ID: row.ID, TenantID: row.TenantID, MailboxID: row.MailboxID, Version: row.Version,
		DisplayName: row.DisplayName, Emails: row.Emails, Phones: row.Phones, Note: row.Note,
	}, nil
}

func (f *fakeStore) PlatformGetContact(_ context.Context, arg store.PlatformGetContactParams) (store.MailboxContacts, error) {
	if err := f.check(arg.TenantID, arg.MailboxID); err != nil {
		return store.MailboxContacts{}, err
	}
	row, ok := f.contacts[arg.ID]
	if !ok {
		return store.MailboxContacts{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeStore) PlatformListContacts(_ context.Context, arg store.PlatformListContactsParams) ([]store.MailboxContacts, error) {
	if err := f.check(arg.TenantID, arg.MailboxID); err != nil {
		return nil, err
	}
	var rows []store.MailboxContacts
	for i := int64(1); i <= f.nextID; i++ {
		if row, ok := f.contacts[i]; ok {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (f *fakeStore) PlatformSearchContacts(_ context.Context, arg store.PlatformSearchContactsParams) ([]store.MailboxContacts, error) {
	return f.PlatformListContacts(context.TODO(), store.PlatformListContactsParams{TenantID: arg.TenantID, MailboxID: arg.MailboxID})
}

func (f *fakeStore) PlatformDeleteContact(_ context.Context, arg store.PlatformDeleteContactParams) (store.PlatformDeleteContactRow, error) {
	if err := f.check(arg.TenantID, arg.MailboxID); err != nil {
		return store.PlatformDeleteContactRow{}, err
	}
	row, ok := f.contacts[arg.ID]
	if !ok || row.Version != arg.Version {
		return store.PlatformDeleteContactRow{}, pgx.ErrNoRows
	}
	delete(f.contacts, arg.ID)
	f.log(arg.TenantID, arg.MailboxID, arg.ID, row.Version+1, true)
	return store.PlatformDeleteContactRow{ID: row.ID, Version: row.Version + 1}, nil
}

func (f *fakeStore) PlatformListChanges(_ context.Context, arg store.PlatformListChangesParams) ([]store.MailboxChanges, error) {
	if err := f.check(arg.TenantID, arg.MailboxID); err != nil {
		return nil, err
	}
	var rows []store.MailboxChanges
	for _, change := range f.changes {
		if change.ID > arg.ID {
			rows = append(rows, change)
		}
	}
	return rows, nil
}

// Calendar methods are unused by the CardDAV adapter; the interface demands them.
func (f *fakeStore) PlatformEnsureCalendar(context.Context, store.PlatformEnsureCalendarParams) (store.MailboxCalendars, error) {
	return store.MailboxCalendars{}, errors.New("not used")
}
func (f *fakeStore) PlatformGetCalendarByName(context.Context, store.PlatformGetCalendarByNameParams) (store.MailboxCalendars, error) {
	return store.MailboxCalendars{}, errors.New("not used")
}
func (f *fakeStore) PlatformInsertEvent(context.Context, store.PlatformInsertEventParams) (store.PlatformInsertEventRow, error) {
	return store.PlatformInsertEventRow{}, errors.New("not used")
}
func (f *fakeStore) PlatformUpdateEvent(context.Context, store.PlatformUpdateEventParams) (store.PlatformUpdateEventRow, error) {
	return store.PlatformUpdateEventRow{}, errors.New("not used")
}
func (f *fakeStore) PlatformGetEvent(context.Context, store.PlatformGetEventParams) (store.MailboxEvents, error) {
	return store.MailboxEvents{}, errors.New("not used")
}
func (f *fakeStore) PlatformListEvents(context.Context, store.PlatformListEventsParams) ([]store.MailboxEvents, error) {
	return nil, errors.New("not used")
}
func (f *fakeStore) PlatformDeleteEvent(context.Context, store.PlatformDeleteEventParams) (store.PlatformDeleteEventRow, error) {
	return store.PlatformDeleteEventRow{}, errors.New("not used")
}

// fakeSessions authenticates exactly one live token for mailbox 42, tenant 7.
type fakeSessions struct {
	valid bool
}

func (s fakeSessions) GetMailboxSession(_ context.Context, _ string) (webmail.Session, error) {
	if !s.valid {
		return webmail.Session{}, errors.New("unknown session")
	}
	return webmail.Session{
		TenantID: 7, MailboxID: 42, Address: "a@search.test",
		ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

const testToken = "token-1"

func newTestHandler(f *fakeStore, sessions fakeSessions) (*Handler, *httptest.Server) {
	h := &Handler{
		Service:  appcollab.Service{Q: f},
		Sessions: sessions,
		Prefix:   "/api/v1/carddav",
	}
	mux := http.NewServeMux()
	mux.Handle("/.well-known/carddav", h)
	mux.Handle("/api/v1/carddav/", h)
	srv := httptest.NewServer(mux)
	return h, srv
}

func authedRequest(t *testing.T, srv *httptest.Server, method, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	if body != "" {
		req.Header.Set("Content-Type", "text/vcard")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := srv.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

const sampleCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada Lovelace\r\nN:Lovelace;Ada;;;\r\nEMAIL;TYPE=INTERNET:ada@example.test\r\nEND:VCARD\r\n"

func TestWellKnownRedirectsToPrincipal(t *testing.T) {
	f := newFakeStore()
	_, srv := newTestHandler(f, fakeSessions{valid: true})
	resp := authedRequest(t, srv, http.MethodGet, "/.well-known/carddav", "", nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/api/v1/carddav/" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestPropfindPrincipalAndCollection(t *testing.T) {
	f := newFakeStore()
	_, srv := newTestHandler(f, fakeSessions{valid: true})
	body := "<D:propfind xmlns:D=\"DAV:\"><D:prop><D:resourcetype/><D:current-user-principal/><D:supported-report-set/></D:prop></D:propfind>"
	resp := authedRequest(t, srv, "PROPFIND", "/api/v1/carddav/", body, map[string]string{"Depth": "0"}) //nolint:bodyclose // closed via readAll/t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207", resp.StatusCode)
	}
	raw := readAll(t, resp)
	for _, want := range []string{"principal", "addressbook-query", "sync-collection"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("propfind response missing %s: %s", want, raw)
		}
	}
	resp = authedRequest(t, srv, "PROPFIND", "/api/v1/carddav/42/", body, map[string]string{"Depth": "1"}) //nolint:bodyclose // closed via readAll/t.Cleanup in authedRequest
	raw = readAll(t, resp)
	if !strings.Contains(raw, "<C:addressbook/>") {
		t.Fatalf("collection propfind missing addressbook: %s", raw)
	}
}

func TestPutGetRoundTripAndETag(t *testing.T) {
	f := newFakeStore()
	_, srv := newTestHandler(f, fakeSessions{valid: true})
	resp := authedRequest(t, srv, http.MethodPut, "/api/v1/carddav/42/new.vcf", sampleCard, nil) //nolint:bodyclose // closed via readAll/t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if location == "" || !strings.HasSuffix(location, ".vcf") {
		t.Fatalf("Location = %q", location)
	}
	etag := strings.Trim(resp.Header.Get("ETag"), "\"")
	if etag != "1" {
		t.Fatalf("ETag = %q, want 1", etag)
	}
	resp = authedRequest(t, srv, http.MethodGet, location, "", nil) //nolint:bodyclose // closed via readAll/t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	card := readAll(t, resp)
	if !strings.Contains(card, "FN:Ada Lovelace") || !strings.Contains(card, "UID:100@urmail") {
		t.Fatalf("GET card = %q", card)
	}
	if resp.Header.Get("ETag") != etagHeaderVersion(1) {
		t.Fatalf("GET ETag = %q", resp.Header.Get("ETag"))
	}
}

func etagHeaderVersion(version int64) string { return "\"" + intToString(version) + "\"" }

func intToString(v int64) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPutIfNoneMatchStarConflicts(t *testing.T) {
	f := newFakeStore()
	_, srv := newTestHandler(f, fakeSessions{valid: true})
	resp := authedRequest(t, srv, http.MethodPut, "/api/v1/carddav/42/new.vcf", sampleCard, nil) //nolint:bodyclose // closed via readAll/t.Cleanup in authedRequest
	location := resp.Header.Get("Location")
	resp = authedRequest(t, srv, http.MethodPut, location, sampleCard, map[string]string{"If-None-Match": "*"}) //nolint:bodyclose // closed via readAll/t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-None-Match status = %d, want 412", resp.StatusCode)
	}
}

func TestPutIfMatchConflictReturns412(t *testing.T) {
	f := newFakeStore()
	_, srv := newTestHandler(f, fakeSessions{valid: true})
	resp := authedRequest(t, srv, http.MethodPut, "/api/v1/carddav/42/new.vcf", sampleCard, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	location := resp.Header.Get("Location")
	resp = authedRequest(t, srv, http.MethodPut, location, sampleCard, map[string]string{"If-Match": "\"999\""}) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match status = %d, want 412", resp.StatusCode)
	}
	resp = authedRequest(t, srv, http.MethodPut, location, sampleCard, map[string]string{"If-Match": "\"1\""}) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("fresh If-Match status = %d, want 204", resp.StatusCode)
	}
	if resp.Header.Get("ETag") != etagHeaderVersion(2) {
		t.Fatalf("updated ETag = %q", resp.Header.Get("ETag"))
	}
}

func TestDeleteTombstonesAndSyncReports404(t *testing.T) {
	f := newFakeStore()
	_, srv := newTestHandler(f, fakeSessions{valid: true})
	resp := authedRequest(t, srv, http.MethodPut, "/api/v1/carddav/42/new.vcf", sampleCard, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	location := resp.Header.Get("Location")
	resp = authedRequest(t, srv, http.MethodDelete, location, "", nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", resp.StatusCode)
	}
	resp = authedRequest(t, srv, http.MethodGet, location, "", nil) //nolint:bodyclose // closed via readAll/t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete = %d, want 404", resp.StatusCode)
	}
	report := "<D:sync-collection xmlns:D=\"DAV:\"><D:sync-token/><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>"
	resp = authedRequest(t, srv, "REPORT", "/api/v1/carddav/42/", report, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	raw := readAll(t, resp)
	if !strings.Contains(raw, "404 Not Found") || !strings.Contains(raw, "sync-token") {
		t.Fatalf("sync report = %s", raw)
	}
}

func TestSyncCollectionOnlyChangesSinceToken(t *testing.T) {
	f := newFakeStore()
	_, srv := newTestHandler(f, fakeSessions{valid: true})
	authedRequest(t, srv, http.MethodPut, "/api/v1/carddav/42/one.vcf", sampleCard, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	report := "<D:sync-collection xmlns:D=\"DAV:\"><D:sync-token/><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>"
	resp := authedRequest(t, srv, "REPORT", "/api/v1/carddav/42/", report, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	raw := readAll(t, resp)
	token := tokenFrom(raw)
	// Second contact after the first sync must be the only delta.
	authedRequest(t, srv, http.MethodPut, "/api/v1/carddav/42/two.vcf", sampleCard, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	sync := "<D:sync-collection xmlns:D=\"DAV:\"><D:sync-token>" + token + "</D:sync-token><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>"
	resp = authedRequest(t, srv, "REPORT", "/api/v1/carddav/42/", sync, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	raw = readAll(t, resp)
	if strings.Count(raw, "<D:response>") != 1 {
		t.Fatalf("delta should contain exactly one change: %s", raw)
	}
}

func tokenFrom(doc string) string {
	marker := "<D:sync-token>"
	start := strings.Index(doc, marker)
	if start < 0 {
		return ""
	}
	start += len(marker)
	end := strings.Index(doc[start:], "</D:sync-token>")
	return doc[start : start+end]
}

func TestAddressbookQueryTextMatch(t *testing.T) {
	f := newFakeStore()
	_, srv := newTestHandler(f, fakeSessions{valid: true})
	authedRequest(t, srv, http.MethodPut, "/api/v1/carddav/42/ada.vcf", sampleCard, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	query := "<C:addressbook-query xmlns:D=\"DAV:\" xmlns:C=\"urn:ietf:params:xml:ns:carddav\"><D:prop><D:getetag/><C:address-data/></D:prop><C:filter><C:prop-filter name=\"FN\"><C:text-match>ada</C:text-match></C:prop-filter></C:filter></C:addressbook-query>"
	resp := authedRequest(t, srv, "REPORT", "/api/v1/carddav/42/", query, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	raw := readAll(t, resp)
	if !strings.Contains(raw, "Ada Lovelace") {
		t.Fatalf("query should match FN case-insensitively: %s", raw)
	}
	miss := strings.Replace(query, "ada", "nomatch", 1)
	resp = authedRequest(t, srv, "REPORT", "/api/v1/carddav/42/", miss, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	raw = readAll(t, resp)
	if strings.Contains(raw, "Ada Lovelace") {
		t.Fatalf("query should not match: %s", raw)
	}
}

func TestUnknownMethodAndReportRejected(t *testing.T) {
	f := newFakeStore()
	_, srv := newTestHandler(f, fakeSessions{valid: true})
	resp := authedRequest(t, srv, "PROPPATCH", "/api/v1/carddav/42/", "", nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PROPPATCH = %d, want 405", resp.StatusCode)
	}
	report := "<D:expand-property xmlns:D=\"DAV:\"/>"
	resp = authedRequest(t, srv, "REPORT", "/api/v1/carddav/42/", report, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unknown report = %d, want 403", resp.StatusCode)
	}
}

func TestAnonymousAndCrossMailboxRejected(t *testing.T) {
	f := newFakeStore()
	_, srv := newTestHandler(f, fakeSessions{valid: false})
	resp := authedRequest(t, srv, "PROPFIND", "/api/v1/carddav/42/", "", nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", resp.StatusCode)
	}
	_, srv2 := newTestHandler(newFakeStore(), fakeSessions{valid: true})
	// Mailbox 43 belongs to someone else; the authenticated principal owns 42.
	resp = authedRequest(t, srv2, "PROPFIND", "/api/v1/carddav/43/", "", nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-mailbox = %d, want 403", resp.StatusCode)
	}
	resp = authedRequest(t, srv2, http.MethodGet, "/api/v1/carddav/43/100.vcf", "", nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-mailbox GET = %d, want 403", resp.StatusCode)
	}
}

func TestMultigetForeignHrefsAre404(t *testing.T) {
	f := newFakeStore()
	_, srv := newTestHandler(f, fakeSessions{valid: true})
	authedRequest(t, srv, http.MethodPut, "/api/v1/carddav/42/ada.vcf", sampleCard, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	report := "<C:addressbook-multiget xmlns:D=\"DAV:\" xmlns:C=\"urn:ietf:params:xml:ns:carddav\"><D:prop><D:getetag/></D:prop>" +
		"<D:href>/api/v1/carddav/42/100.vcf</D:href><D:href>/api/v1/carddav/43/100.vcf</D:href></C:addressbook-multiget>"
	resp := authedRequest(t, srv, "REPORT", "/api/v1/carddav/42/", report, nil) //nolint:bodyclose // closed via t.Cleanup in authedRequest
	raw := readAll(t, resp)
	if strings.Count(raw, "200 OK") != 1 || strings.Count(raw, "404 Not Found") != 1 {
		t.Fatalf("multiget = %s", raw)
	}
}
