package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type fakeContactStore struct {
	books    []store.MailboxAddressbooks
	contacts []store.PlatformInsertContactRow
	nextID   int64
}

func (f *fakeContactStore) PlatformEnsureAddressbook(_ context.Context, arg store.PlatformEnsureAddressbookParams) (store.MailboxAddressbooks, error) {
	for _, book := range f.books {
		if book.TenantID == arg.TenantID && book.MailboxID == arg.MailboxID && book.Name == arg.Name {
			return store.MailboxAddressbooks{}, pgx.ErrNoRows
		}
	}
	f.nextID++
	book := store.MailboxAddressbooks{ID: f.nextID, TenantID: arg.TenantID, MailboxID: arg.MailboxID, Name: arg.Name, Version: 1}
	f.books = append(f.books, book)
	return book, nil
}
func (f *fakeContactStore) PlatformGetAddressbookByName(_ context.Context, arg store.PlatformGetAddressbookByNameParams) (store.MailboxAddressbooks, error) {
	for _, book := range f.books {
		if book.TenantID == arg.TenantID && book.MailboxID == arg.MailboxID && book.Name == arg.Name {
			return book, nil
		}
	}
	return store.MailboxAddressbooks{}, pgx.ErrNoRows
}
func (f *fakeContactStore) PlatformInsertContact(_ context.Context, arg store.PlatformInsertContactParams) (store.PlatformInsertContactRow, error) {
	f.nextID++
	row := store.PlatformInsertContactRow{ID: f.nextID, TenantID: arg.TenantID, MailboxID: arg.MailboxID, AddressbookID: arg.AddressbookID, Version: 1, DisplayName: arg.DisplayName, Emails: arg.Emails}
	f.contacts = append(f.contacts, row)
	return row, nil
}
func (f *fakeContactStore) PlatformUpdateContact(context.Context, store.PlatformUpdateContactParams) (store.PlatformUpdateContactRow, error) {
	return store.PlatformUpdateContactRow{}, pgx.ErrNoRows
}
func (f *fakeContactStore) PlatformGetContact(context.Context, store.PlatformGetContactParams) (store.MailboxContacts, error) {
	return store.MailboxContacts{}, pgx.ErrNoRows
}
func (f *fakeContactStore) PlatformListContacts(_ context.Context, arg store.PlatformListContactsParams) ([]store.MailboxContacts, error) {
	var out []store.MailboxContacts
	for _, row := range f.contacts {
		if row.TenantID == arg.TenantID && row.MailboxID == arg.MailboxID {
			out = append(out, store.MailboxContacts{ID: row.ID, TenantID: row.TenantID, MailboxID: row.MailboxID, DisplayName: row.DisplayName, Version: row.Version, Emails: row.Emails})
		}
	}
	return out, nil
}
func (f *fakeContactStore) PlatformSearchContacts(context.Context, store.PlatformSearchContactsParams) ([]store.MailboxContacts, error) {
	return nil, nil
}
func (f *fakeContactStore) PlatformDeleteContact(context.Context, store.PlatformDeleteContactParams) (store.PlatformDeleteContactRow, error) {
	return store.PlatformDeleteContactRow{}, pgx.ErrNoRows
}
func (f *fakeContactStore) PlatformEnsureCalendar(context.Context, store.PlatformEnsureCalendarParams) (store.MailboxCalendars, error) {
	return store.MailboxCalendars{}, pgx.ErrNoRows
}
func (f *fakeContactStore) PlatformGetCalendarByName(context.Context, store.PlatformGetCalendarByNameParams) (store.MailboxCalendars, error) {
	return store.MailboxCalendars{}, pgx.ErrNoRows
}
func (f *fakeContactStore) PlatformInsertEvent(context.Context, store.PlatformInsertEventParams) (store.PlatformInsertEventRow, error) {
	return store.PlatformInsertEventRow{}, pgx.ErrNoRows
}
func (f *fakeContactStore) PlatformUpdateEvent(context.Context, store.PlatformUpdateEventParams) (store.PlatformUpdateEventRow, error) {
	return store.PlatformUpdateEventRow{}, pgx.ErrNoRows
}
func (f *fakeContactStore) PlatformGetEvent(context.Context, store.PlatformGetEventParams) (store.MailboxEvents, error) {
	return store.MailboxEvents{}, pgx.ErrNoRows
}
func (f *fakeContactStore) PlatformListEvents(context.Context, store.PlatformListEventsParams) ([]store.MailboxEvents, error) {
	return nil, nil
}
func (f *fakeContactStore) PlatformDeleteEvent(context.Context, store.PlatformDeleteEventParams) (store.PlatformDeleteEventRow, error) {
	return store.PlatformDeleteEventRow{}, pgx.ErrNoRows
}
func (f *fakeContactStore) PlatformListChanges(context.Context, store.PlatformListChangesParams) ([]store.MailboxChanges, error) {
	return nil, nil
}

func TestMailboxContactsRequireSessionAndStayInMailbox(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	sessions := &fakeMailboxSessions{}
	store := &fakeContactStore{}
	native := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash}, Sessions: sessions, Vault: webmail.NewMemoryVault()}
	contacts := MailboxContactsHandler{Service: appcollab.Service{Q: store}}
	r := newNativeTestRouter(t, native, sessions)
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	auth.GET("/contacts", contacts.ListContacts)
	auth.POST("/contacts", contacts.CreateContact)

	open := httptest.NewRecorder()
	r.ServeHTTP(open, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/contacts", nil))
	if open.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", open.Code)
	}
	token := loginNative(t, r, "alice@example.test", password)
	body := bytes.NewBufferString(`{"display_name":"Ada","emails":[{"address":"Ada@Example.Test"}]}`)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/contacts", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	r.ServeHTTP(created, req)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if len(store.contacts) != 1 || store.contacts[0].MailboxID != 9 || store.contacts[0].TenantID != 7 {
		t.Fatalf("stored=%+v", store.contacts)
	}
	listReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/contacts", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listed := httptest.NewRecorder()
	r.ServeHTTP(listed, listReq)
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte("Ada")) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
}

func TestExportContactsVCFStreamsAllContacts(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	sessions := &fakeMailboxSessions{}
	emails, _ := json.Marshal([]collab.Email{{Address: "ada@example.test"}})
	seed := &fakeContactStore{}
	seed.nextID = 1
	seed.contacts = []store.PlatformInsertContactRow{
		{ID: 1, TenantID: 7, MailboxID: 9, AddressbookID: 1, Version: 1, DisplayName: "Ada", Emails: emails},
		{ID: 2, TenantID: 7, MailboxID: 9, AddressbookID: 1, Version: 1, DisplayName: "Bob"},
	}
	native := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash}, Sessions: sessions, Vault: webmail.NewMemoryVault()}
	contacts := MailboxContactsHandler{Service: appcollab.Service{Q: seed}}
	r := newNativeTestRouter(t, native, sessions)
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	auth.GET("/contacts/export.vcf", contacts.ExportContactsVCF)
	token := loginNative(t, r, "alice@example.test", password)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/contacts/export.vcf", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Count(body, "BEGIN:VCARD") != 2 || !strings.Contains(body, "Ada") || !strings.Contains(body, "ada@example.test") {
		t.Fatalf("unexpected vcard body: %q", body)
	}
}
