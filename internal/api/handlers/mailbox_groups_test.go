package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestAddGroupMemberRejectsAnotherAddressBook(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/members", func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: 1, MailboxID: 2})
		MailboxContactsHandler{GroupStore: zeroGroupStore{}}.AddGroupMember(c)
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/members", strings.NewReader(`{"group_addressbook_id":1,"contact_addressbook_id":2,"group_id":3,"contact_id":4}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "CONTACT_GROUP_SCOPE") {
		t.Fatal(w.Code, w.Body.String())
	}
}

type zeroGroupStore struct{}

func (zeroGroupStore) PlatformInsertContactGroupMember(context.Context, store.PlatformInsertContactGroupMemberParams) (int64, error) {
	return 0, nil
}
