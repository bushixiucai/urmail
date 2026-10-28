package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type managedFolderFixture struct {
	*fakeIMAPClient
	mutations     int
	transferError bool
}

func (f *managedFolderFixture) CreateFolder(_ context.Context, name string) error {
	f.mutations++
	f.folders = append(f.folders, webmail.Folder{Name: name, Delimiter: "/"})
	return nil
}
func (f *managedFolderFixture) RenameFolder(_ context.Context, _, _ string) error {
	f.mutations++
	return nil
}
func (f *managedFolderFixture) DeleteFolder(_ context.Context, _ string) error {
	f.mutations++
	return nil
}
func (f *managedFolderFixture) TransferMessages(_ context.Context, _ string, uids []uint32, to string, _ bool) (webmail.TransferResult, error) {
	f.mutations++
	result := webmail.TransferResult{Destination: to, UIDValidity: 12, Results: []webmail.TransferItem{{UID: uids[0], Status: "success", DestinationUID: 50}}}
	if f.transferError {
		result.Results[0].Status = "unknown"
		result.Results[0].DestinationUID = 0
		return result, errors.New("lost acknowledgement")
	}
	return result, nil
}

func TestFolderManagementGuardsAndUncertainOutcomes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := &managedFolderFixture{fakeIMAPClient: &fakeIMAPClient{folders: []webmail.Folder{{Name: "INBOX"}, {Name: "Drafts", Flags: []string{`\Drafts`}}, {Name: "Custom", Delimiter: "/"}, {Name: "Trash", Flags: []string{`\Trash`}}}}}
	vault := webmail.NewMemoryVault()
	vault.Put("token", "password")
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: &fakeMailboxSessions{}, Vault: vault, Dialer: &fakeDialer{wantAddress: "alice@example.test", client: f}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: 7, MailboxID: 9, Address: "alice@example.test"})
		c.Set(middleware.ContextMailboxTokenHash, "token")
	})
	router.POST("/folders", h.CreateFolder)
	router.PATCH("/folders/:name", h.RenameFolder)
	router.DELETE("/folders/:name", h.DeleteFolder)
	router.POST("/folders/:name/messages/copy", h.CopyMessages)
	router.POST("/folders/:name/messages/trash", h.TrashMessages)
	router.POST("/folders/:name/messages/restore", h.RestoreMessages)
	request := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "unit-transfer-request")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s status %d: %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	request("POST", "/folders", `{"name":"New 项目"}`, 201)
	count := f.mutations
	request("POST", "/folders", `{"name":"INBOX"}`, 400)
	request("PATCH", "/folders/Drafts", `{"name":"Renamed","uid_validity":9}`, 403)
	request("PATCH", "/folders/Custom", `{"name":"Renamed","uid_validity":8}`, 409)
	request("POST", "/folders/INBOX/messages/copy", `{"uids":[1],"to":"Custom"}`, 400)
	request("POST", "/folders/INBOX/messages/trash", `{"uids":[1,1],"uid_validity":9}`, 400)
	request("POST", "/folders/Custom/messages/restore", `{"uids":[1],"uid_validity":9}`, 503)
	f.listed.Total = 1
	request("DELETE", "/folders/Custom?uid_validity=9", "", 409)
	f.listed.Total = 0
	f.folders = append(f.folders, webmail.Folder{Name: "Custom/Child"})
	request("DELETE", "/folders/Custom?uid_validity=9", "", 409)
	if f.mutations != count {
		t.Fatal("rejected operation mutated IMAP")
	}
	request("POST", "/folders/INBOX/messages/trash", `{"uids":[1],"uid_validity":9}`, 503)
	if f.mutations != count {
		t.Fatal("missing durable journal allowed an IMAP side effect")
	}
}

// Simulate another IMAP client's APPEND immediately after the empty search.
// The endpoint must retain this content under Trash rather than issue DELETE.
type lateAppendFolderFixture struct {
	*managedFolderFixture
	messages  map[string][]string
	recovered string
	deleted   bool
}

func (f *lateAppendFolderFixture) ListMessages(context.Context, string, webmail.ListOpts) (webmail.ListResult, error) {
	f.messages["Custom"] = []string{"late MIME attachment payload"}
	return webmail.ListResult{Total: 0}, nil
}
func (f *lateAppendFolderFixture) RenameFolder(_ context.Context, from, to string) error {
	f.messages[to] = f.messages[from]
	delete(f.messages, from)
	f.recovered = to
	return nil
}
func (f *lateAppendFolderFixture) DeleteFolder(_ context.Context, name string) error {
	f.deleted = true
	delete(f.messages, name)
	return nil
}
func TestFolderDeletionPreservesConcurrentLateArrival(t *testing.T) {
	f := &lateAppendFolderFixture{managedFolderFixture: &managedFolderFixture{fakeIMAPClient: &fakeIMAPClient{folders: []webmail.Folder{{Name: "Custom", Delimiter: "."}, {Name: "Waste", Delimiter: ".", Flags: []string{`\Trash`}}}}}, messages: map[string][]string{}}
	vault := webmail.NewMemoryVault()
	vault.Put("token", "password")
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: &fakeMailboxSessions{}, Vault: vault, Dialer: &fakeDialer{wantAddress: "alice@example.test", client: f}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: 7, MailboxID: 9, Address: "alice@example.test"})
		c.Set(middleware.ContextMailboxTokenHash, "token")
	})
	router.DELETE("/folders/:name", h.DeleteFolder)
	request := httptest.NewRequestWithContext(t.Context(), "DELETE", "/folders/Custom?uid_validity=9", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || f.deleted || !strings.HasPrefix(f.recovered, "Waste.Deleted-") || len(f.messages[f.recovered]) != 1 || f.messages[f.recovered][0] != "late MIME attachment payload" {
		t.Fatalf("status=%d deleted=%v destination=%s messages=%v body=%s", response.Code, f.deleted, f.recovered, f.messages, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"recoverable":true`) || !strings.Contains(response.Body.String(), `"destination":"Waste.Deleted-`) {
		t.Fatal(response.Body.String())
	}
}
