package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/imageproxy"
)

// cannedFetcher serves fixed bytes for every URL.
type cannedFetcher struct {
	data        []byte
	contentType string
	err         error
	seen        []string
}

func (f *cannedFetcher) Fetch(_ context.Context, rawURL string) ([]byte, string, error) {
	f.seen = append(f.seen, rawURL)
	if f.err != nil {
		return nil, "", f.err
	}
	return f.data, f.contentType, nil
}

func imageProxyRouter(t *testing.T, handler *MailboxNativeHandler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/mailbox/images/proxy", handler.ImageProxy)
	return router
}

func TestImageProxyServesSignedImage(t *testing.T) {
	signer := imageproxy.NewSigner([]byte("render-key"))
	token, signature, err := signer.Sign(imageproxy.Payload{
		MailboxID: 9,
		UID:       42,
		Expires:   time.Now().Add(time.Hour).Unix(),
		URL:       "https://img.example/logo.png",
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	fetcher := &cannedFetcher{data: []byte("png-bytes"), contentType: "image/png"}
	handler := &MailboxNativeHandler{RenderKey: []byte("render-key"), ImageFetcher: fetcher}
	router := imageProxyRouter(t, handler)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/images/proxy?u="+url.QueryEscape(token)+"&sig="+url.QueryEscape(signature), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "png-bytes" {
		t.Fatalf("body %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content type %q", got)
	}
	if len(fetcher.seen) != 1 || fetcher.seen[0] != "https://img.example/logo.png" {
		t.Fatalf("fetcher saw %v", fetcher.seen)
	}
}

func TestImageProxyRejectsUnsignedAndExpiredRequests(t *testing.T) {
	signer := imageproxy.NewSigner([]byte("render-key"))
	handler := &MailboxNativeHandler{RenderKey: []byte("render-key"), ImageFetcher: &cannedFetcher{}}
	router := imageProxyRouter(t, handler)

	valid, validSig, err := signer.Sign(imageproxy.Payload{
		MailboxID: 9, UID: 42, Expires: time.Now().Add(time.Hour).Unix(), URL: "https://img.example/logo.png",
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	expired, expiredSig, err := signer.Sign(imageproxy.Payload{
		MailboxID: 9, UID: 42, Expires: time.Now().Add(-time.Hour).Unix(), URL: "https://img.example/logo.png",
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	foreignSigner := imageproxy.NewSigner([]byte("another-key"))
	foreign, foreignSig, err := foreignSigner.Sign(imageproxy.Payload{
		MailboxID: 9, UID: 42, Expires: time.Now().Add(time.Hour).Unix(), URL: "https://img.example/logo.png",
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	cases := []struct {
		name  string
		query string
	}{
		{"missing signature", "u=" + valid},
		{"missing token", "sig=" + validSig},
		{"wrong signature", "u=" + valid + "&sig=" + strings.Repeat("a", len(validSig))},
		{"expired signature", "u=" + expired + "&sig=" + expiredSig},
		{"foreign key", "u=" + foreign + "&sig=" + foreignSig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/images/proxy?"+tc.query, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// A bearer token that is present must belong to the signed mailbox: one
// mailbox may not spend another mailbox's signature.
func TestImageProxyRejectsForeignBearer(t *testing.T) {
	signer := imageproxy.NewSigner([]byte("render-key"))
	token, signature, err := signer.Sign(imageproxy.Payload{
		MailboxID: 9, UID: 42, Expires: time.Now().Add(time.Hour).Unix(), URL: "https://img.example/logo.png",
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sessions := &fakeMailboxSessions{}
	handler := &MailboxNativeHandler{RenderKey: []byte("render-key"), Sessions: sessions, ImageFetcher: &cannedFetcher{}}
	router := imageProxyRouter(t, handler)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/images/proxy?u="+token+"&sig="+signature, nil)
	req.Header.Set("Authorization", "Bearer some-other-mailbox-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign bearer accepted: %d", rec.Code)
	}
}

// A failed fetch degrades to a transparent placeholder so the message
// layout keeps its shape.
func TestImageProxyDegradesToPlaceholder(t *testing.T) {
	signer := imageproxy.NewSigner([]byte("render-key"))
	token, signature, err := signer.Sign(imageproxy.Payload{
		MailboxID: 9, UID: 42, Expires: time.Now().Add(time.Hour).Unix(), URL: "http://169.254.169.254/secret",
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	fetcher := &cannedFetcher{err: errors.New("blocked")}
	handler := &MailboxNativeHandler{RenderKey: []byte("render-key"), ImageFetcher: fetcher}
	router := imageProxyRouter(t, handler)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/images/proxy?u="+token+"&sig="+signature, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("placeholder type %q", got)
	}
	if rec.Body.Len() != len(imageproxy.PlaceholderPNG) {
		t.Fatalf("placeholder size %d", rec.Body.Len())
	}
}

// Without a signing key the endpoint stays closed: nothing may be proxied.
func TestImageProxyDisabledWithoutKey(t *testing.T) {
	handler := &MailboxNativeHandler{ImageFetcher: &cannedFetcher{}}
	router := imageProxyRouter(t, handler)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/images/proxy?u=x&sig=y", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}
