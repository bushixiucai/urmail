package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

// OPS-008 closeout: the well-known CalDAV discovery path must exist and point
// clients at the calendar home, and the standard ActiveSync endpoint must be
// mounted with the same handler contract as /api/v1/eas.
func TestCalDAVWellKnownRedirectsToCalendarHome(t *testing.T) {
	r := New(Options{Logger: zap.NewNop()})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/.well-known/caldav", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("well-known caldav status = %d, want 301", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/caldav/" {
		t.Fatalf("Location = %q, want /caldav/", loc)
	}
}

func TestActiveSyncAliasEndpointMounted(t *testing.T) {
	r := New(Options{Logger: zap.NewNop()})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/Microsoft-Server-ActiveSync", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	// Without an EAS backend the handler reports the capability boundary
	// (401 for unauthenticated devices) instead of gin's 404 for a missing
	// route — the alias must exist, not bypass authentication.
	if w.Code == http.StatusNotFound {
		t.Fatalf("ActiveSync alias not mounted: %d %s", w.Code, w.Body.String())
	}
}
