package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestOversizedPublicLoginIsRejectedBeforeStoreOrHash(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/login", NewAuthHandler(nil).Login)
	payload, _ := json.Marshal(map[string]string{"email": "unknown@example.test", "password": strings.Repeat("x", 4097)})
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/login", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
}
func TestMFACapacityExpiryAndCredentialGeneration(t *testing.T) {
	capacityCount := func() float64 {
		t.Helper()
		metrics, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, metric := range metrics {
			if metric.GetName() == "urmail_auth_mfa_capacity_rejections_total" {
				if len(metric.Metric) != 1 || len(metric.Metric[0].Label) != 0 {
					t.Fatal("MFA capacity counter must have no identity labels")
				}
				return metric.Metric[0].GetCounter().GetValue()
			}
		}
		t.Fatal("missing MFA capacity counter")
		return 0
	}
	before := capacityCount()
	now := time.Now()
	s := newMFATokenStore()
	s.now = func() time.Time { return now }
	s.maxEntries = 2
	s.maxPerUser = 1
	if _, err := s.issue(1, "a", 7); err != nil {
		t.Fatal(err)
	}
	if got := capacityCount(); got != before {
		t.Fatalf("successful MFA issue increased capacity rejections: before=%v got=%v", before, got)
	}
	if _, err := s.issue(1, "a", 7); err == nil {
		t.Fatal("per-user capacity")
	}
	if _, err := s.issue(2, "b", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.issue(3, "c", 0); err == nil {
		t.Fatal("global capacity")
	}
	if got := capacityCount(); got != before+2 {
		t.Fatalf("MFA capacity rejections: want=%v got=%v", before+2, got)
	}
	now = now.Add(mfaTokenTTL)
	s.mu.Lock()
	s.pruneLocked()
	s.mu.Unlock()
	if len(s.entries) != 0 {
		t.Fatal("abandoned challenges retained")
	}
	token, err := s.issue(1, "a", 8)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := s.check(token)
	if !ok || entry.CredentialVersion != 8 {
		t.Fatal("generation not bound")
	}
	s.revokeUser(1)
	if _, ok = s.take(token); ok {
		t.Fatal("revoked challenge consumed")
	}
}
func TestMFASetupWithoutSecretsCannotPersistPlaintext(t *testing.T) {
	st := newFakeAuthStore()
	id := st.addUser(t, "mfa-no-key@example.test", false, "", nil)
	h := NewAuthHandler(st)
	r := gin.New()
	r.POST("/setup", func(c *gin.Context) {
		c.Set(middleware.ContextUserID, id)
		c.Set(middleware.ContextSession, store.Sessions{ID: "current", UserID: id})
		h.MFASetup(c)
	})
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/setup", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || st.users[id].MfaSecret.Valid {
		t.Fatal("missing key wrote a seed")
	}
}

type failingPanelLogout struct{ *fakeAuthStore }

func (failingPanelLogout) DeleteSession(context.Context, string) error {
	return errors.New("injected revocation failure")
}

type failingMailboxLogout struct{ *fakeMailboxSessions }

func (failingMailboxLogout) Revoke(context.Context, string) error {
	return errors.New("injected revocation failure")
}

func TestLogoutDoesNotAcknowledgeFailedRevocation(t *testing.T) {
	panel := NewAuthHandler(failingPanelLogout{newFakeAuthStore()})
	router := gin.New()
	router.POST("/panel", func(c *gin.Context) {
		c.Set(middleware.ContextSession, store.Sessions{ID: "session", UserID: 1})
		panel.Logout(c)
	})
	vault := webmail.NewMemoryVault()
	vault.Put( //nolint:staticcheck // test adapter: Put is the interface under test
		"mailbox-session", "password")
	native := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: failingMailboxLogout{&fakeMailboxSessions{}}, Vault: vault}
	router.POST("/mailbox", func(c *gin.Context) {
		c.Set(middleware.ContextMailboxTokenHash, "mailbox-session")
		native.DeleteSession(c)
	})
	for _, path := range []string{"/panel", "/mailbox"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "POST", path, nil))
		if w.Code != 503 || strings.Contains(w.Body.String(), `"revoked":true`) || strings.Contains(w.Body.String(), `"status":"ok"`) {
			t.Fatalf("failed revocation acknowledged: %s %d %s", path, w.Code, w.Body.String())
		}
	}
	if _, ok := vault.Get("mailbox-session"); ok {
		t.Fatal("failed durable revoke retained local plaintext")
	}
}
