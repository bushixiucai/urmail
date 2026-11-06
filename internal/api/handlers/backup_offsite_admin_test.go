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
	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/store"
)

const offsiteTestSecret = "super-secret-access-key-9f2a"

func newOffsiteAdminRouter(t *testing.T) (*gin.Engine, *store.Queries, context.Context) {
	t.Helper()
	p, _ := authIntegrationDatabase(t)
	q := store.New(p)
	h := &BackupOffsiteAdminHandler{Store: q, Secrets: authFixtureSecrets()}
	r := gin.New()
	r.GET("/offsite", h.Get)
	r.PUT("/offsite", middleware.RequirePlatformTx(p, zap.NewNop()), h.Put)
	r.POST("/offsite/test-connection", h.TestConnection)
	return r, q, context.Background()
}

func callOffsite(ctx context.Context, t *testing.T, r *gin.Engine, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	path := "/offsite"
	if method == http.MethodPost {
		path = "/offsite/test-connection"
	}
	req := httptest.NewRequestWithContext(ctx, method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// TestOffsiteDestinationLifecycle covers the admin contract: explicit
// disabled state when unconfigured, encrypted persistence, redacted
// responses, disable-preserves-secrets and explicit clearing.
func TestOffsiteDestinationLifecycle(t *testing.T) {
	r, q, ctx := newOffsiteAdminRouter(t)

	// Unconfigured must be an explicit, credential-free disabled state.
	if w := callOffsite(ctx, t, r, http.MethodGet, ""); w.Code != 200 || strings.Contains(w.Body.String(), "enabled\":true") {
		t.Fatalf("unconfigured status %d %s", w.Code, w.Body.String())
	}

	putBody := `{"type":"s3","bucket":"gen","prefix":"urmail","endpoint":"http://minio.internal:9000","enabled":true,` +
		`"credentials":{"s3":{"access_key_id":"` + offsiteTestSecret + `","secret_key":"secondary-secret","region":"us-east-1"}}}`
	w := callOffsite(ctx, t, r, http.MethodPut, putBody)
	if w.Code != 200 {
		t.Fatalf("put %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), offsiteTestSecret) || strings.Contains(w.Body.String(), "secondary-secret") {
		t.Fatalf("put response leaks credentials: %s", w.Body.String())
	}

	// The persisted value must be ciphertext: no plaintext anywhere.
	raw, err := q.PlatformGetSetting(ctx, backupauto.DestinationSettingsKey)
	if err != nil {
		t.Fatal(err)
	}
	stored := string(raw.Value)
	if !strings.Contains(stored, "enc:v1:") {
		t.Fatalf("stored destination must hold enc:v1: credentials: %s", stored)
	}
	if strings.Contains(stored, offsiteTestSecret) || strings.Contains(stored, "secondary-secret") {
		t.Fatal("stored destination leaks plaintext credentials")
	}

	// GET reports presence flags only.
	w = callOffsite(ctx, t, r, http.MethodGet, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"has_credentials":true`) {
		t.Fatalf("get %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), offsiteTestSecret) || strings.Contains(w.Body.String(), "enc:v1") {
		t.Fatalf("get response leaks credentials: %s", w.Body.String())
	}

	// Disabling without new credentials preserves the stored blob so
	// re-enabling never requires re-entering secrets.
	w = callOffsite(ctx, t, r, http.MethodPut, `{"type":"s3","bucket":"gen","endpoint":"http://minio.internal:9000","enabled":false}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"has_credentials":true`) {
		t.Fatalf("disable %d %s", w.Code, w.Body.String())
	}
	raw, err = q.PlatformGetSetting(ctx, backupauto.DestinationSettingsKey)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw.Value), "enc:v1:") {
		t.Fatal("disable must preserve the stored credential ciphertext")
	}

	// Enabling without credentials is rejected: it would store a broken
	// destination.
	w = callOffsite(ctx, t, r, http.MethodPut, `{"type":"sftp","endpoint":"host:22","enabled":true}`)
	if w.Code != 400 {
		t.Fatalf("enable without credentials must 400, got %d", w.Code)
	}

	// type "" clears the destination entirely.
	w = callOffsite(ctx, t, r, http.MethodPut, `{"type":""}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatalf("clear %d %s", w.Code, w.Body.String())
	}
	if _, err := q.PlatformGetSetting(ctx, backupauto.DestinationSettingsKey); err == nil {
		t.Fatal("cleared destination row must be gone")
	}
}

func TestOffsiteDestinationValidationFailures(t *testing.T) {
	r, _, ctx := newOffsiteAdminRouter(t)
	for name, body := range map[string]string{
		"unknown type":    `{"type":"ftp","endpoint":"h"}`,
		"sftp no pin":     `{"type":"sftp","endpoint":"h:22","enabled":true,"credentials":{"sftp_user":"u","sftp":{"password":"p"}}}`,
		"s3 no keys":      `{"type":"s3","bucket":"b","enabled":true,"credentials":{"s3":{"access_key_id":"","secret_key":""}}}`,
		"bad webdav auth": `{"type":"webdav","endpoint":"https://u:p@dav/","enabled":true,"credentials":{"webdav":{"username":"u","password":"p"}}}`,
	} {
		if w := callOffsite(ctx, t, r, http.MethodPut, body); w.Code != 400 {
			t.Fatalf("%s must be rejected with 400, got %d", name, w.Code)
		}
	}
}

// TestOffsiteTestConnectionFailureIsObservable exercises the fail-closed
// probe path: an unreachable endpoint answers ok:false with an endpoint-
// level error and never a credential value.
func TestOffsiteTestConnectionFailureIsObservable(t *testing.T) {
	r, _, ctx := newOffsiteAdminRouter(t)
	body := `{"type":"webdav","endpoint":"http://127.0.0.1:1/","prefix":"x","enabled":true,` +
		`"credentials":{"webdav":{"username":"` + offsiteTestSecret + `","password":"pw"}}}`
	w := callOffsite(ctx, t, r, http.MethodPost, body)
	if w.Code != 200 {
		t.Fatalf("test-connection status %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Fatalf("unreachable endpoint must answer ok:false: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), offsiteTestSecret) {
		t.Fatal("test-connection response leaks credentials")
	}
	// Empty body tests the saved destination; nothing configured → explicit.
	w = callOffsite(ctx, t, r, http.MethodPost, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "destination not configured") {
		t.Fatalf("unconfigured test-connection %d %s", w.Code, w.Body.String())
	}
}
