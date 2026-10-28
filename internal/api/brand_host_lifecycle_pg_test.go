package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/panelpassword"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/net/dns/dnsmessage"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// A real loopback UDP DNS server supplies disposable TXT answers; no public DNS
// authority or ACME success is inferred from this isolated provider boundary.
func TestBrandHostProductionHTTPOwnershipAndRevocationPG(t *testing.T) {
	app, platform := remediationHTTPDatabase(t)
	ctx := t.Context()
	var brand, other, uid int64
	for _, item := range []struct {
		sql string
		out *int64
	}{{"INSERT INTO brands(slug,name) VALUES('brand-proof','Brand proof') RETURNING id", &brand}, {"INSERT INTO brands(slug,name) VALUES('brand-other','Other') RETURNING id", &other}, {"INSERT INTO users(email,password_hash) VALUES('brand-proof-admin@example.test','fixture') RETURNING id", &uid}} {
		if e := platform.QueryRow(ctx, item.sql).Scan(item.out); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := platform.Exec(ctx, "INSERT INTO platform_admins(user_id) VALUES($1)", uid); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("URMAIL_BRAND_BROWSER_FIXTURE") != "" {
		password, hashErr := panelpassword.Hash("Brand-Fixture-Local-2026!")
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		if _, hashErr = platform.Exec(ctx, "UPDATE users SET password_hash=$1 WHERE id=$2", password, uid); hashErr != nil {
			t.Fatal(hashErr)
		}
	}
	q := store.New(platform)
	user, e := q.GetUserByID(ctx, uid)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateSession(ctx, store.CreateSessionParams{ID: middleware.HashSessionToken("brand-proof-token"), UserID: uid, ExpectedHash: user.PasswordHash, ExpectedVersion: user.CredentialVersion, CsrfToken: "proof-csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); e != nil {
		t.Fatal(e)
	}
	conn, e := (&net.ListenConfig{}).ListenPacket(ctx, "udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	var mu sync.Mutex
	txt := "wrong-proof"
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			var parser dnsmessage.Parser
			header, err := parser.Start(buf[:n])
			if err != nil {
				continue
			}
			question, err := parser.Question()
			if err != nil {
				continue
			}
			builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: header.ID, Response: true, Authoritative: true})
			_ = builder.StartQuestions()
			_ = builder.Question(question)
			_ = builder.StartAnswers()
			mu.Lock()
			answer := txt
			if directory := os.Getenv("URMAIL_BRAND_BROWSER_FIXTURE"); directory != "" {
				if raw, readErr := os.ReadFile(filepath.Join(directory, "dns-txt")); readErr == nil {
					answer = strings.TrimSpace(string(raw))
				}
			}
			mu.Unlock()
			_ = builder.TXTResource(dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: 1}, dnsmessage.TXTResource{TXT: []string{answer}})
			response, err := builder.Finish()
			if err == nil {
				_, _ = conn.WriteTo(response, addr)
			}
		}
	}()
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", conn.LocalAddr().String())
	}}
	engine := api.New(api.Options{AppPool: app, PlatformPool: platform, MyHostname: func() string {
		if os.Getenv("URMAIL_BRAND_BROWSER_FIXTURE") != "" {
			return "127.0.0.1:17321"
		}
		return "box.example.test"
	}(), BrandHostTXTLookup: resolver.LookupTXT, Audit: q})
	server := httptest.NewServer(engine)
	defer server.Close()
	if directory := os.Getenv("URMAIL_BRAND_BROWSER_FIXTURE"); directory != "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"url": server.URL, "brand_id": brand, "email": "brand-proof-admin@example.test", "password": "Brand-Fixture-Local-2026!"})
		if err := os.WriteFile(filepath.Join(directory, "fixture.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		deadline := time.NewTimer(12 * time.Minute)
		defer deadline.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-deadline.C:
				t.Fatal("browser fixture timeout")
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(directory, "done")); err == nil {
					return
				}
			}
		}
	}

	call := func(method, path, host, token, csrf string, body any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, method, server.URL+"/api/v1"+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Origin", "https://"+host)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, res.StatusCode, want, b)
		}
		out := map[string]any{}
		_ = json.Unmarshal(b, &out)
		return out
	}
	invoke := func(action string, want int) map[string]any {
		return call("POST", "/platform/brand-hosts/brand.example.test/lifecycle", "box.example.test", "brand-proof-token", "proof-csrf", map[string]any{"action": action, "confirm": true}, want)
	}
	bind := map[string]any{"hostname": "brand.example.test", "brand_id": brand}
	call("POST", "/platform/brand-hosts", "box.example.test", "brand-proof-token", "proof-csrf", bind, 200)
	call("POST", "/platform/brand-hosts", "box.example.test", "brand-proof-token", "proof-csrf", map[string]any{"hostname": "brand.example.test", "brand_id": other}, 409)
	call("POST", "/platform/brand-hosts/brand.example.test/lifecycle", "box.example.test", "brand-proof-token", "", map[string]any{"action": "challenge", "confirm": true}, 403)
	invoke("activate", 409)
	proof := invoke("challenge", 200)
	invoke("verify", 409)
	mu.Lock()
	txt = proof["value"].(string)
	mu.Unlock()
	invoke("verify", 200)
	invoke("verify", 409)
	state, err := q.PlatformGetBrandHostLifecycle(ctx, "brand.example.test")
	if err != nil || state.Status != "verified" || !state.VerifiedAt.Valid {
		t.Fatal(state, err)
	}
	consumed, err := q.PlatformGetBrandHostChallenge(ctx, "brand.example.test")
	if err != nil || !consumed.ConsumedAt.Valid {
		t.Fatal("proof not consumed", err)
	}
	// The absent privileged capability must not create a false active state.
	invoke("activate", 503)
	call("GET", "/public/branding", "brand.example.test", "", "", nil, 421)
	call("DELETE", "/platform/brand-hosts/brand.example.test", "box.example.test", "brand-proof-token", "proof-csrf", nil, 200)
	state, err = q.PlatformGetBrandHostLifecycle(ctx, "brand.example.test")
	if err != nil || state.FailureReason.String != "revoked" || state.VerifiedAt.Valid {
		t.Fatal("revocation did not erase live proof", state, err)
	}
	call("GET", "/public/branding", "brand.example.test", "", "", nil, 421)
	call("POST", "/platform/brand-hosts", "box.example.test", "brand-proof-token", "proof-csrf", bind, 200)
	// Rebinding must require a new challenge: consumed earlier proof cannot verify.
	invoke("verify", 409)
	_ = agentd.OpBrandHostActivate
}
