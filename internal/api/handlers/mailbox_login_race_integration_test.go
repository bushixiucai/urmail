package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type pausedNativeSessionStore struct {
	StoreMailboxNative
	once    sync.Once
	entered chan struct{}
	resume  chan struct{}
}

func (s *pausedNativeSessionStore) Create(ctx context.Context, token, address, expected string, remember bool) (webmail.Session, error) {
	s.once.Do(func() {
		close(s.entered)
		select {
		case <-s.resume:
		case <-ctx.Done():
		}
	})
	return s.StoreMailboxNative.Create(ctx, token, address, expected, remember)
}

func TestNativeLoginCannotIssueTokenAfterPasswordProofBecomesStale(t *testing.T) {
	scenario := newMailboxTenant(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	q := store.New(scenario.p)
	oldPassword, newPassword := "original-mail-password", "replacement-mail-password"
	oldHash, err := mail.HashPassword(oldPassword)
	if err != nil {
		t.Fatal(err)
	}
	newHash, err := mail.HashPassword(newPassword)
	if err != nil {
		t.Fatal(err)
	}
	var domainID, mailboxID int64
	var fqdn string
	if err = scenario.p.QueryRow(ctx, "SELECT id,fqdn FROM domains WHERE tenant_id=$1 ORDER BY id LIMIT 1", scenario.tid).Scan(&domainID, &fqdn); err != nil {
		t.Fatal(err)
	}
	address := "race@" + fqdn
	if err = scenario.p.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'race',$3,$4,128,'active') RETURNING id", scenario.tid, domainID, address, oldHash).Scan(&mailboxID); err != nil {
		t.Fatal(err)
	}
	principal := StoreMailboxNative{Q: q}
	paused := &pausedNativeSessionStore{StoreMailboxNative: principal, entered: make(chan struct{}), resume: make(chan struct{})}
	vault := webmail.NewMemoryVault()
	handler := &MailboxNativeHandler{Enabled: true, Auth: principal, Sessions: paused, Vault: vault}
	router := gin.New()
	router.POST("/login", handler.CreateSession)
	login := func(password string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]string{"email": address, "password": password})
		req := httptest.NewRequestWithContext(ctx, "POST", "/login", strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- login(oldPassword) }()
	select {
	case <-paused.entered:
	case <-ctx.Done():
		t.Fatal("login did not reach post-password barrier")
	}
	if _, err = scenario.p.Exec(ctx, "UPDATE mailboxes SET password_hash=$3 WHERE tenant_id=$1 AND id=$2", scenario.tid, mailboxID, newHash); err != nil {
		close(paused.resume)
		t.Fatal(err)
	}
	close(paused.resume)
	var result *httptest.ResponseRecorder
	select {
	case result = <-done:
	case <-ctx.Done():
		t.Fatal("stale login did not complete")
	}
	if result.Code != 401 || strings.Contains(result.Body.String(), `"token"`) {
		t.Fatalf("stale password minted token: %d %s", result.Code, result.Body.String())
	}
	var count int
	if err = scenario.p.QueryRow(ctx, "SELECT count(*) FROM mailbox_sessions WHERE tenant_id=$1 AND mailbox_id=$2", scenario.tid, mailboxID).Scan(&count); err != nil || count != 0 {
		t.Fatal("stale login persisted session", count, err)
	}
	if result = login(newPassword); result.Code != 200 {
		t.Fatalf("new password rejected: %d %s", result.Code, result.Body.String())
	}
}
