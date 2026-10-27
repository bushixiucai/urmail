package eas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// newTestRouter builds a gin engine with the session pre-seeded, mirroring
// the middleware context the real router provides.
func newTestRouter(sess *webmail.Session) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.OPTIONS("/api/v1/mailbox/eas", func(c *gin.Context) {
		if sess != nil {
			c.Set(middleware.ContextMailboxSession, *sess)
		}
		(&Server{}).HandleOptions(c)
	})
	r.POST("/api/v1/mailbox/eas", func(c *gin.Context) {
		if sess != nil {
			c.Set(middleware.ContextMailboxSession, *sess)
		}
		(&Server{}).HandleCommand(c)
	})
	return r
}

func do(r *gin.Engine, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestOptionsAdvertisesImplementedSubset pins the negotiation headers.
func TestOptionsAdvertisesImplementedSubset(t *testing.T) {
	w := do(newTestRouter(&webmail.Session{TenantID: 1, MailboxID: 1}),
		http.MethodOptions, "/api/v1/mailbox/eas", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if got := w.Header().Get(headerVersions); got != AdvertisedVersions {
		t.Fatalf("versions %q", got)
	}
	if got := w.Header().Get(headerCommands); got != AdvertisedCommands {
		t.Fatalf("commands %q", got)
	}
}

// TestOptionsRejectsAnonymous keeps the mailbox principal boundary.
func TestOptionsRejectsAnonymous(t *testing.T) {
	if w := do(newTestRouter(nil), http.MethodOptions, "/api/v1/mailbox/eas", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
}

// TestCommandValidations covers version, device id and command gates that
// run before any database access.
func TestCommandValidations(t *testing.T) {
	sess := &webmail.Session{TenantID: 1, MailboxID: 1}
	r := newTestRouter(sess)
	cases := []struct {
		name string
		url  string
		hdr  map[string]string
		want int
	}{
		{"unsupported version", "/api/v1/mailbox/eas?Cmd=FolderSync&DeviceId=d1",
			map[string]string{headerVersion: "16.2"}, http.StatusBadRequest},
		{"revision number as version", "/api/v1/mailbox/eas?Cmd=FolderSync&DeviceId=d1",
			map[string]string{headerVersion: "28.0"}, http.StatusBadRequest},
		{"2.5 not implemented", "/api/v1/mailbox/eas?Cmd=FolderSync&DeviceId=d1",
			map[string]string{headerVersion: "2.5"}, http.StatusBadRequest},
		{"missing device id", "/api/v1/mailbox/eas?Cmd=FolderSync",
			map[string]string{headerVersion: "14.1"}, http.StatusBadRequest},
		{"unknown command", "/api/v1/mailbox/eas?Cmd=Bogus&DeviceId=d1",
			map[string]string{headerVersion: "14.1"}, http.StatusBadRequest},
		{"refused GetItemEstimate", "/api/v1/mailbox/eas?Cmd=GetItemEstimate&DeviceId=d1",
			map[string]string{headerVersion: "14.1"}, http.StatusForbidden},
		{"refused Search", "/api/v1/mailbox/eas?Cmd=Search&DeviceId=d1",
			map[string]string{headerVersion: "14.1"}, http.StatusForbidden},
		{"refused MoveItems", "/api/v1/mailbox/eas?Cmd=MoveItems&DeviceId=d1",
			map[string]string{headerVersion: "14.1"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		if w := do(r, http.MethodPost, tc.url, tc.hdr); w.Code != tc.want {
			t.Fatalf("%s: status %d want %d", tc.name, w.Code, tc.want)
		}
	}
	// Anonymous POST is rejected before anything else.
	if w := do(newTestRouter(nil), http.MethodPost,
		"/api/v1/mailbox/eas?Cmd=FolderSync&DeviceId=d1", nil); w.Code != http.StatusUnauthorized {
		t.Fatal("anonymous accepted")
	}
}

// TestNotImplementableSyncStatus pins the documented refusal of the default
// Sync backend: Status 4 (protocol error), never a fake success.
func TestNotImplementableSyncStatus(t *testing.T) {
	resp, err := NotImplementableSync{}.HandleSync(t.Context(),
		&SyncRequest{Node: Element(pageAirSync, tokSync)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ChildText(tokStatus) != "4" {
		t.Fatalf("status %q", resp.ChildText(tokStatus))
	}
	if resp.Child(tokCollections) != nil {
		t.Fatal("fake collections present")
	}
}
