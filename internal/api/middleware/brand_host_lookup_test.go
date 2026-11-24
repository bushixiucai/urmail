package middleware

// Shared-core unit tests for the brand boundary lookup contract: a lookup
// failure must fail closed with the fixed 503 DB_ERROR body (and must NOT
// leak engine detail), only an explicit not-found passes through, and the
// active/inactive/eligibility semantics match the original PG behavior.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type fakeBrandLookup struct {
	brandID        int64
	status         string
	found          bool
	lifecycleErr   error
	eligible       bool
	eligibleErr    error
	lifecycleCalls int
	eligibleCalls  int
}

func (f *fakeBrandLookup) BrandHostLifecycle(_ context.Context, host string) (int64, string, bool, error) {
	f.lifecycleCalls++
	if f.lifecycleErr != nil {
		return 0, "", false, f.lifecycleErr
	}
	if !f.found {
		return 0, "", false, nil
	}
	return f.brandID, f.status, true, nil
}

func (f *fakeBrandLookup) BrandHostCertEligible(_ context.Context, host string) (bool, error) {
	f.eligibleCalls++
	if f.eligibleErr != nil {
		return false, f.eligibleErr
	}
	return f.eligible, nil
}

func runBoundary(t *testing.T, lookup BrandHostLookup, host string) (*httptest.ResponseRecorder, *gin.Context, bool) {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	reached := false
	var ctx *gin.Context
	r := gin.New()
	r.Use(BrandHostBoundaryLookup(lookup))
	r.GET("/", func(c *gin.Context) {
		reached = true
		ctx = c
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = host
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w, ctx, reached
}

func TestBrandBoundaryLookupErrorFailsClosed(t *testing.T) {
	lookup := &fakeBrandLookup{lifecycleErr: errors.New("boom: secret engine detail")}
	w, _, reached := runBoundary(t, lookup, "brand.example.test")
	if reached {
		t.Fatal("lookup error passed the boundary through")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "DB_ERROR") {
		t.Fatalf("body = %s, want fixed DB_ERROR code", body)
	}
	// The failure detail must not leak into the response.
	if strings.Contains(body, "boom") || strings.Contains(body, "secret") {
		t.Fatalf("body leaks engine detail: %s", body)
	}
	if lookup.eligibleCalls != 0 {
		t.Fatalf("eligibility queried %d times after lifecycle error", lookup.eligibleCalls)
	}
}

func TestBrandBoundaryUnknownHostPassesThrough(t *testing.T) {
	lookup := &fakeBrandLookup{found: false}
	w, ctx, reached := runBoundary(t, lookup, "unknown.example.test")
	if !reached {
		t.Fatal("unknown host did not pass through")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if _, branded := ctx.Get(ContextBrandID); branded {
		t.Fatal("unknown host must not set a brand scope")
	}
	if lookup.eligibleCalls != 0 {
		t.Fatalf("eligibility queried %d times for unknown host", lookup.eligibleCalls)
	}
}

func TestBrandBoundaryActiveHostScopesRequest(t *testing.T) {
	lookup := &fakeBrandLookup{found: true, brandID: 9, status: "active", eligible: true}
	w, ctx, reached := runBoundary(t, lookup, "brand.example.test")
	if !reached || w.Code != http.StatusOK {
		t.Fatalf("active brand host blocked: reached=%v code=%d", reached, w.Code)
	}
	brand, branded := ctx.Get(ContextBrandID)
	if !branded || brand.(int64) != 9 {
		t.Fatalf("brand scope = %v (set=%v), want 9", brand, branded)
	}
}

func TestBrandBoundaryInactiveHostRejected(t *testing.T) {
	lookup := &fakeBrandLookup{found: true, brandID: 9, status: "failed", eligible: true}
	w, _, reached := runBoundary(t, lookup, "brand.example.test")
	if reached {
		t.Fatal("inactive brand host reached the handler")
	}
	if w.Code != http.StatusMisdirectedRequest || !strings.Contains(w.Body.String(), "BRAND_HOST_INACTIVE") {
		t.Fatalf("code = %d body = %s, want 421 BRAND_HOST_INACTIVE", w.Code, w.Body.String())
	}
}

func TestBrandBoundaryCertEligibilityFailuresRejected(t *testing.T) {
	for name, mutate := range map[string]func(*fakeBrandLookup){
		"not-eligible":      func(f *fakeBrandLookup) { f.eligible = false },
		"eligibility-error": func(f *fakeBrandLookup) { f.eligibleErr = errors.New("boom") },
	} {
		t.Run(name, func(t *testing.T) {
			lookup := &fakeBrandLookup{found: true, brandID: 9, status: "active"}
			mutate(lookup)
			w, _, reached := runBoundary(t, lookup, "brand.example.test")
			if reached {
				t.Fatal("ineligible brand host reached the handler")
			}
			if w.Code != http.StatusMisdirectedRequest {
				t.Fatalf("code = %d, want 421", w.Code)
			}
		})
	}
}
