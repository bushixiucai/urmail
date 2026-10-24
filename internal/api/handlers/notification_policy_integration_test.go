package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestReminderPolicyValidatesAndAuditsChangesAtomically(t *testing.T) {
	p, _ := authIntegrationDatabase(t)
	ctx := context.Background()
	q := store.New(p)
	var actor int64
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('reminder-policy@example.test','unused') RETURNING id").Scan(&actor); err != nil {
		t.Fatal(err)
	}
	h := &NotificationConfigHandler{Store: q}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.ContextUserID, actor) })
	r.GET("/policy", h.ReminderPolicy)
	r.PUT("/policy", middleware.RequirePlatformTx(p, zap.NewNop()), h.SaveReminderPolicy)
	call := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, method, "/policy", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	if w := call(http.MethodGet, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "[7,3,1,0]") {
		t.Fatalf("defaults %d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{"days":null}`, `{"days":[-1]}`, `{"days":[366]}`, `{"days":[0.5]}`} {
		if w := call(http.MethodPut, body); w.Code != 400 {
			t.Fatalf("invalid policy %s: %d", body, w.Code)
		}
	}
	if w := call(http.MethodPut, `{"days":[0,7,7,3]}`); w.Code != 200 || !strings.Contains(w.Body.String(), "[0,3,7]") {
		t.Fatalf("canonical policy %d %s", w.Code, w.Body.String())
	}
	if _, err := p.Exec(ctx, "CREATE FUNCTION reject_policy_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture audit failure'; END $$; CREATE TRIGGER reject_policy_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_policy_audit()"); err != nil {
		t.Fatal(err)
	}
	if w := call(http.MethodPut, `{"days":[]}`); w.Code != 500 {
		t.Fatalf("audit failure status %d", w.Code)
	}
	raw, err := q.PlatformGetSubscriptionNoticeDays(ctx)
	if err != nil || string(raw) != "[0, 3, 7]" {
		t.Fatalf("audit failure changed policy %s %v", raw, err)
	}
}
