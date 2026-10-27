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

type missingGroupStore struct{}

func (missingGroupStore) PlatformInsertContactGroup(context.Context, store.PlatformInsertContactGroupParams) (store.MailboxContactGroups, error) {
	return store.MailboxContactGroups{}, nil
}
func (missingGroupStore) PlatformListContactGroups(context.Context, store.PlatformListContactGroupsParams) ([]store.MailboxContactGroups, error) {
	return nil, nil
}
func (missingGroupStore) PlatformDeleteContactGroup(context.Context, store.PlatformDeleteContactGroupParams) (bool, error) {
	return false, nil
}

func TestDeleteMissingContactGroupReturnsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := MailboxContactsHandler{Groups: missingGroupStore{}}
	r.DELETE("/groups/:id", func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: 1, MailboxID: 2})
		h.DeleteGroup(c)
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/groups/9", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "CONTACT_GROUP_NOT_FOUND") {
		t.Fatal(w.Code, w.Body.String())
	}
}
