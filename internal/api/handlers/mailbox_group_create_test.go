package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type groupCreateStore struct{ err error }

func (s groupCreateStore) PlatformInsertContactGroup(context.Context, store.PlatformInsertContactGroupParams) (store.MailboxContactGroups, error) {
	return store.MailboxContactGroups{}, s.err
}
func (groupCreateStore) PlatformListContactGroups(context.Context, store.PlatformListContactGroupsParams) ([]store.MailboxContactGroups, error) {
	return nil, nil
}
func (groupCreateStore) PlatformDeleteContactGroup(context.Context, store.PlatformDeleteContactGroupParams) (bool, error) {
	return false, nil
}

func TestCreateGroupMissingAddressBookIsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := MailboxContactsHandler{Groups: groupCreateStore{err: pgx.ErrNoRows}}
	r.POST("/groups", func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: 1, MailboxID: 2})
		h.CreateGroup(c)
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/groups", strings.NewReader(`{"addressbook_id":4,"name":"A"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "CONTACT_GROUP_NOT_FOUND") {
		t.Fatal(w.Code, w.Body.String())
	}
	_ = errors.New("")
}

func TestCreateGroupDuplicateReturnsConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := MailboxContactsHandler{Groups: groupCreateStore{err: &pgconn.PgError{Code: "23505"}}}
	r.POST("/groups", func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: 1, MailboxID: 2})
		h.CreateGroup(c)
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/groups", strings.NewReader(`{"addressbook_id":4,"name":"A"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "CONFLICT") {
		t.Fatal(w.Code, w.Body.String())
	}
}
