package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestRegistrationRequiresContactProofAndDoesNotGiftMail(t *testing.T) {
	p, _ := authIntegrationDatabase(t)
	ctx := context.Background()
	if _, err := p.Exec(ctx, "INSERT INTO brands(slug,name) VALUES('default','Default') ON CONFLICT(slug) DO NOTHING"); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	h := &RegistrationHandler{Pool: p, Secrets: box, BaseURL: "https://panel.example.test", Enabled: true}
	r := gin.New()
	r.POST("/register", h.Register)
	r.POST("/verify", h.Verify)
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	started := post("/register", `{"email":"new@example.test","password":"a-long-new-account-password","name":"New tenant"}`)
	if started.Code != 202 {
		t.Fatalf("register: %d %s", started.Code, started.Body.String())
	}
	var count int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM users WHERE email='new@example.test'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("account active before contact proof: %d %v", count, err)
	}
	var encrypted string
	if err := p.QueryRow(ctx, "SELECT secret_payload FROM notification_outbox WHERE kind='contact_verification'").Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encrypted, "enc:v1:") {
		t.Fatal("verification credential was not encrypted")
	}
	token, err := box.DecryptString(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	verified := post("/verify", `{"token":"`+token+`"}`)
	if verified.Code != 201 {
		t.Fatalf("verify: %d %s", verified.Code, verified.Body.String())
	}
	var data struct {
		TenantID int64 `json:"tenant_id"`
	}
	if err = json.Unmarshal(verified.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	var allowed bool
	if err = p.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", data.TenantID).Scan(&allowed); err != nil || allowed {
		t.Fatalf("self-service gifted mail: %v %v", allowed, err)
	}
	if err = p.QueryRow(ctx, "SELECT count(*) FROM subscriptions WHERE tenant_id=$1 AND status IN('active','trialing','past_due')", data.TenantID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("self-service gifted subscription: %d %v", count, err)
	}
	eligible, err := store.New(p).SelfServicePurchaseAllowed(ctx, store.SelfServicePurchaseAllowedParams{TenantID: data.TenantID, ID: 0})
	if err != nil || eligible.Bool {
		t.Fatalf("purchase without domain proof: %v %v", eligible, err)
	}
	replay := post("/verify", `{"token":"`+token+`"}`)
	if replay.Code != 400 {
		t.Fatalf("proof replay accepted: %d", replay.Code)
	}
}
