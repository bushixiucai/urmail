package imageproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// stubResolver returns fixed addresses for every host.
type stubResolver struct {
	addrs []netip.Addr
	err   error
}

func (r stubResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.addrs, nil
}

// stubTransport serves canned responses and records the requests it saw.
type stubTransport struct {
	responses map[string]*stubResponse
	requests  []*http.Request
}

type stubResponse struct {
	status      int
	contentType string
	body        string
	location    string
}

func (t *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, req)
	key := req.URL.String()
	canned, ok := t.responses[key]
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	}
	header := http.Header{}
	if canned.contentType != "" {
		header.Set("Content-Type", canned.contentType)
	}
	if canned.location != "" {
		header.Set("Location", canned.location)
	}
	return &http.Response{
		StatusCode: canned.status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(canned.body)),
		Request:    req,
	}, nil
}

// tinyPNG is a valid 1x1 PNG used as the "remote image" in tests.
var tinyPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
	0x42, 0x60, 0x82,
}

func publicResolver() stubResolver {
	return stubResolver{addrs: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
}

func TestSignerRoundTrip(t *testing.T) {
	signer := NewSigner([]byte("k"))
	token, signature, err := signer.Sign(Payload{MailboxID: 7, UID: 42, URL: "https://img.example/a.png"})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	payload, err := signer.Verify(token, signature)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if payload.MailboxID != 7 || payload.UID != 42 || payload.URL != "https://img.example/a.png" {
		t.Fatalf("payload mismatch: %+v", payload)
	}
	if payload.Expires <= time.Now().Unix() {
		t.Fatalf("expiry not filled: %+v", payload)
	}
}

func TestSignerRejectsTamperedAndForeignTokens(t *testing.T) {
	signer := NewSigner([]byte("k"))
	token, signature, err := signer.Sign(Payload{MailboxID: 7, UID: 42, URL: "https://img.example/a.png"})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := signer.Verify(token+"x", signature); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered token accepted: %v", err)
	}
	if _, err := signer.Verify(token, signature+"x"); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered signature accepted: %v", err)
	}
	other := NewSigner([]byte("other-key"))
	if _, err := other.Verify(token, signature); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("foreign key accepted: %v", err)
	}
	if _, err := signer.Verify("", ""); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("empty token accepted: %v", err)
	}
}

func TestSignerRejectsExpiredToken(t *testing.T) {
	signer := NewSigner([]byte("k"))
	token, signature, err := signer.Sign(Payload{
		MailboxID: 7,
		UID:       42,
		URL:       "https://img.example/a.png",
		Expires:   time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := signer.Verify(token, signature); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired token accepted: %v", err)
	}
}

func TestSignerWithoutKeyIsDisabled(t *testing.T) {
	signer := NewSigner(nil)
	if signer.Enabled() {
		t.Fatal("signer with empty key reports enabled")
	}
	if _, _, err := signer.Sign(Payload{MailboxID: 1, URL: "https://img.example/a.png"}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
}

// The SSRF screen must refuse every address that only exists inside the
// deployment's own network, and every scheme that is not plain HTTP.
func TestFetcherBlocksPrivateTargets(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"loopback v4", "http://127.0.0.1/a.png"},
		{"loopback v6", "http://[::1]/a.png"},
		{"private class A", "http://10.1.2.3/a.png"},
		{"private class B", "http://172.16.9.9/a.png"},
		{"private class C", "http://192.168.1.1/a.png"},
		{"link local", "http://169.254.169.254/latest/meta-data"},
		{"carrier nat", "http://100.64.0.1/a.png"},
		{"unspecified", "http://0.0.0.0/a.png"},
		{"multicast", "http://224.0.0.1/a.png"},
		{"benchmark range", "http://198.18.0.1/a.png"},
		{"file scheme", "file:///etc/passwd"},
		{"ftp scheme", "ftp://example.com/a.png"},
		{"data scheme", "data:image/png;base64,AAAA"},
		{"odd port", "http://example.com:8080/a.png"},
		{"credentials in url", "http://user:pass@example.com/a.png"},
		{"single label host", "http://intranet/a.png"},
		{"internal suffix", "http://images.internal/a.png"},
		{"local suffix", "http://printer.local/a.png"},
	}
	fetcher := &Fetcher{Resolver: publicResolver()}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := fetcher.Fetch(context.Background(), tc.url); !errors.Is(err, ErrBlocked) {
				t.Fatalf("%s was not blocked: %v", tc.url, err)
			}
		})
	}
}

// A host that resolves to a private address is refused even when the
// public address is listed first.
func TestFetcherBlocksPrivateDNSAnswer(t *testing.T) {
	fetcher := &Fetcher{Resolver: stubResolver{addrs: []netip.Addr{
		netip.MustParseAddr("93.184.216.34"),
		netip.MustParseAddr("10.0.0.7"),
	}}}
	if _, _, err := fetcher.Fetch(context.Background(), "https://img.example/a.png"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("mixed answer not blocked: %v", err)
	}
}

func TestFetcherFollowsRedirectsButRescreensEachHop(t *testing.T) {
	transport := &stubTransport{responses: map[string]*stubResponse{
		"https://img.example/a.png": {status: http.StatusFound, location: "http://169.254.169.254/secret"},
	}}
	fetcher := &Fetcher{Resolver: publicResolver(), Transport: transport}
	if _, _, err := fetcher.Fetch(context.Background(), "https://img.example/a.png"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("redirect into link-local not blocked: %v", err)
	}
}

func TestFetcherAllowsPublicImage(t *testing.T) {
	transport := &stubTransport{responses: map[string]*stubResponse{
		"https://img.example/a.png": {status: http.StatusOK, contentType: "image/png", body: string(tinyPNG)},
	}}
	fetcher := &Fetcher{Resolver: publicResolver(), Transport: transport}
	data, contentType, err := fetcher.Fetch(context.Background(), "https://img.example/a.png")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if contentType != "image/png" || len(data) != len(tinyPNG) {
		t.Fatalf("unexpected result: %s %d bytes", contentType, len(data))
	}
	if len(transport.requests) != 1 {
		t.Fatalf("expected one request, got %d", len(transport.requests))
	}
	req := transport.requests[0]
	if got := req.Header.Get("User-Agent"); got != UserAgent {
		t.Fatalf("user agent %q leaks or is missing", got)
	}
	if got := req.Header.Get("Referer"); got != "" {
		t.Fatalf("referer must not be sent, got %q", got)
	}
}

func TestFetcherRefusesNonImageAndSpoofedTypes(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
	}{
		{"svg", "image/svg+xml", "<svg xmlns='http://www.w3.org/2000/svg'></svg>"},
		{"html", "text/html", "<html><body>hi</body></html>"},
		{"no type", "", string(tinyPNG)},
		{"spoofed png", "image/png", "<html>not a png</html>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := &stubTransport{responses: map[string]*stubResponse{
				"https://img.example/a.png": {status: http.StatusOK, contentType: tc.contentType, body: tc.body},
			}}
			fetcher := &Fetcher{Resolver: publicResolver(), Transport: transport}
			if _, _, err := fetcher.Fetch(context.Background(), "https://img.example/a.png"); err == nil {
				t.Fatal("non-image payload accepted")
			}
		})
	}
}

func TestFetcherEnforcesSizeLimit(t *testing.T) {
	transport := &stubTransport{responses: map[string]*stubResponse{
		"https://img.example/a.png": {status: http.StatusOK, contentType: "image/png", body: string(tinyPNG) + strings.Repeat("x", 100)},
	}}
	fetcher := &Fetcher{Resolver: publicResolver(), Transport: transport, MaxBytes: 10}
	if _, _, err := fetcher.Fetch(context.Background(), "https://img.example/a.png"); err == nil {
		t.Fatal("oversized image accepted")
	}
}

func TestFetcherRejectsNonOKStatus(t *testing.T) {
	transport := &stubTransport{responses: map[string]*stubResponse{
		"https://img.example/a.png": {status: http.StatusInternalServerError, contentType: "image/png", body: string(tinyPNG)},
	}}
	fetcher := &Fetcher{Resolver: publicResolver(), Transport: transport}
	if _, _, err := fetcher.Fetch(context.Background(), "https://img.example/a.png"); err == nil {
		t.Fatal("error status accepted")
	}
}
