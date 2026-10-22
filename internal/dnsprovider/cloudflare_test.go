package dnsprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloudflareEnsureTXTCreatesRecord(t *testing.T) {
	var created bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/zones") && r.URL.Path == "/zones":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"zone-1"}]}`))
		case strings.HasSuffix(r.URL.Path, "/dns_records") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
		case strings.HasSuffix(r.URL.Path, "/dns_records") && r.Method == http.MethodPost:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload["name"] != "_urmail-verify.example.test" {
				t.Errorf("record name = %v", payload["name"])
			}
			created = true
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"record-1"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()

	c := NewCloudflare("token")
	c.Base = srv.URL
	if err := c.EnsureTXT(context.Background(), "example.test", "_urmail-verify.example.test", "abc"); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("DNS record was not created")
	}
}
