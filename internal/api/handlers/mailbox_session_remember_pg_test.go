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
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// A remembered login extends the mailbox session to ~30 days; a plain login
// keeps the 30-minute TTL. Both stay bound to password_version: rotating the
// mailbox password invalidates the session immediately.
func TestMailboxSessionRememberExtendsTTLOnly(t *testing.T) {
	scenario := newMailboxTenant(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	q := store.New(scenario.p)
	hash, err := mail.HashPassword("remember-ttl-password")
	if err != nil {
		t.Fatal(err)
	}
	var domainID int64
	var fqdn string
	if err = scenario.p.QueryRow(ctx, "SELECT id,fqdn FROM domains WHERE tenant_id=$1 ORDER BY id LIMIT 1", scenario.tid).Scan(&domainID, &fqdn); err != nil {
		t.Fatal(err)
	}
	address := "remember@" + fqdn
	var mailboxID int64
	if err = scenario.p.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'remember',$3,$4,128,'active') RETURNING id", scenario.tid, domainID, address, hash).Scan(&mailboxID); err != nil {
		t.Fatal(err)
	}
	principal := StoreMailboxNative{Q: q}
	vault := webmail.NewMemoryVault()
	handler := &MailboxNativeHandler{Enabled: true, Auth: principal, Sessions: principal, Vault: vault}
	router := gin.New()
	router.POST("/api/v1/mailbox/session", handler.CreateSession)

	login := func(remember bool) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"email": address, "password": "remember-ttl-password", "remember": remember})
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/mailbox/session", strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("login %d %s", w.Code, w.Body.String())
		}
		var created struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Token == "" {
			t.Fatalf("decode session: %v body %s", err, w.Body.String())
		}
		expires, err := time.Parse(time.RFC3339, created.ExpiresAt)
		if err != nil {
			t.Fatalf("expires_at %q: %v", created.ExpiresAt, err)
		}
		return created.Token + "|" + expires.Format(time.RFC3339Nano)
	}

	// Plain login: ~30 minutes.
	parts := strings.SplitN(login(false), "|", 2)
	expires, _ := time.Parse(time.RFC3339Nano, parts[1])
	if ttl := time.Until(expires); ttl <= 25*time.Minute || ttl > 35*time.Minute {
		t.Fatalf("plain session ttl=%v want ~30m", ttl)
	}

	// Remembered login: ~30 days.
	parts = strings.SplitN(login(true), "|", 2)
	longToken := parts[0]
	expires, _ = time.Parse(time.RFC3339Nano, parts[1])
	if ttl := time.Until(expires); ttl < 29*24*time.Hour || ttl > 30*24*time.Hour+time.Hour {
		t.Fatalf("remembered session ttl=%v want ~30d", ttl)
	}

	// The remembered session resolves while the password is unchanged.
	longHash := mailboxprincipal.HashToken(longToken)
	if _, err = principal.Get(ctx, longHash); err != nil {
		t.Fatalf("remembered session not resolvable: %v", err)
	}

	// Password rotation must still revoke the remembered session immediately.
	newHash, err := mail.HashPassword("rotated-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = scenario.p.Exec(ctx, "UPDATE mailboxes SET password_hash=$1 WHERE id=$2", newHash, mailboxID); err != nil {
		t.Fatal(err)
	}
	if _, err = principal.Get(ctx, longHash); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("remembered session survived password rotation: err=%v", err)
	}
}
