package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestCanonicalHostAllowedSemantics pins the contract the router relies on:
// an unset canonical host (development stacks) allows the request, while a
// configured canonical host rejects nonstandard ports and foreign hosts.
func TestCanonicalHostAllowedSemantics(t *testing.T) {
	newContext := func(host string, canonical ...string) *gin.Context {
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		request.Host = host
		c.Request = request
		if len(canonical) > 0 && canonical[0] != "" {
			c.Set(canonicalOriginKey, canonical[0])
		}
		return c
	}

	if !CanonicalHostAllowed(newContext("127.0.0.1:8080")) {
		t.Fatal("unset canonical must allow the request (development stacks)")
	}
	if CanonicalHostAllowed(newContext("127.0.0.1:8080", "box.example.test")) {
		t.Fatal("dev loopback host must not satisfy a configured canonical host")
	}
	if CanonicalHostAllowed(newContext("box.example.test:8080", "box.example.test")) {
		t.Fatal("canonical host on a nonstandard port must be rejected")
	}
	if CanonicalHostAllowed(newContext("brand.example.test", "box.example.test")) {
		t.Fatal("brand hosts must not satisfy the canonical host")
	}
	if !CanonicalHostAllowed(newContext("box.example.test", "box.example.test")) {
		t.Fatal("exact canonical host must be allowed")
	}
	if !CanonicalHostAllowed(newContext("box.example.test:443", "box.example.test")) {
		t.Fatal("canonical host on the default HTTPS port must be allowed")
	}
}
