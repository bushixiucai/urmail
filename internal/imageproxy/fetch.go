package imageproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	// MaxImageBytes bounds one proxied image body.
	MaxImageBytes = 5 << 20
	// MaxRedirects bounds how many redirects are followed. Every hop is
	// screened again; a redirect into a private range is refused.
	MaxRedirects = 3
	// DefaultTimeout bounds one complete fetch (DNS, connect, TLS, body).
	DefaultTimeout = 10 * time.Second
	// dialTimeout bounds the TCP connect itself.
	dialTimeout = 5 * time.Second
	// UserAgent identifies the proxy to third-party hosts. It carries no
	// reader information.
	UserAgent = "urMail-Image-Proxy/1.0"
)

// ErrBlocked reports a target that the SSRF screen refuses. The message is
// deliberately coarse: it never repeats the address that was rejected.
var ErrBlocked = errors.New("imageproxy: target blocked")

// allowedImageTypes is the response type allowlist. SVG is deliberately
// absent: an SVG document can script, so it never crosses the proxy.
var allowedImageTypes = map[string]string{
	"image/png":  "image/png",
	"image/jpeg": "image/jpeg",
	"image/gif":  "image/gif",
	"image/webp": "image/webp",
}

// PlaceholderPNG is a 1x1 fully transparent PNG. It is served when a fetch
// fails so that a blocked or broken image leaves a hole in the layout
// instead of a broken-image box.
var PlaceholderPNG = []byte{
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

// Resolver resolves host names to addresses.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Fetcher downloads remote images with SSRF protection. The zero value is
// usable and applies the default limits.
type Fetcher struct {
	// MaxBytes overrides MaxImageBytes when positive.
	MaxBytes int64
	// Timeout overrides DefaultTimeout when positive.
	Timeout time.Duration
	// Resolver resolves host names. Tests inject a stub.
	Resolver Resolver
	// Transport performs the request. Tests inject a stub that serves
	// canned responses without touching the network.
	Transport http.RoundTripper
}

func (f *Fetcher) maxBytes() int64 {
	if f.MaxBytes > 0 {
		return f.MaxBytes
	}
	return MaxImageBytes
}

func (f *Fetcher) timeout() time.Duration {
	if f.Timeout > 0 {
		return f.Timeout
	}
	return DefaultTimeout
}

func (f *Fetcher) resolver() Resolver {
	if f.Resolver != nil {
		return f.Resolver
	}
	return net.DefaultResolver
}

// Fetch downloads one remote image and returns its bytes and media type.
// The target is screened before any socket is opened, redirects are
// followed manually so that every hop is screened again, and the response
// must be an allowlisted raster image.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout())
	defer cancel()

	current := rawURL
	for hop := 0; hop <= MaxRedirects; hop++ {
		target, addrs, err := f.screen(ctx, current)
		if err != nil {
			return nil, "", err
		}
		resp, err := f.get(ctx, target, addrs)
		if err != nil {
			return nil, "", err
		}
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			location := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if location == "" {
				return nil, "", fmt.Errorf("imageproxy: redirect without location")
			}
			next, err := target.Parse(location)
			if err != nil {
				return nil, "", fmt.Errorf("imageproxy: bad redirect target")
			}
			current = next.String()
			continue
		case resp.StatusCode != http.StatusOK:
			_ = resp.Body.Close()
			return nil, "", fmt.Errorf("imageproxy: unexpected status %d", resp.StatusCode)
		}
		defer func() { _ = resp.Body.Close() }()
		return readImage(resp, f.maxBytes())
	}
	return nil, "", fmt.Errorf("imageproxy: too many redirects")
}

// screen validates the target URL and resolves it to public addresses. It
// returns the parsed URL and the addresses that may be dialed.
func (f *Fetcher) screen(ctx context.Context, rawURL string) (*url.URL, []netip.Addr, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, nil, fmt.Errorf("imageproxy: bad url")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, nil, ErrBlocked
	}
	if u.User != nil {
		return nil, nil, ErrBlocked
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return nil, nil, ErrBlocked
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if port != "80" && port != "443" {
		return nil, nil, ErrBlocked
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if !isPublicAddr(addr) {
			return nil, nil, ErrBlocked
		}
		return u, []netip.Addr{addr.Unmap()}, nil
	}
	if blockedHost(host) {
		return nil, nil, ErrBlocked
	}
	addrs, err := f.resolver().LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil, nil, fmt.Errorf("imageproxy: resolve failed")
	}
	// Every candidate address must be public: a host that mixes a public
	// and a private address is refused rather than picked from.
	out := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		if !isPublicAddr(addr) {
			return nil, nil, ErrBlocked
		}
		out = append(out, addr.Unmap())
	}
	return u, out, nil
}

// get performs one HTTP request against a screened target. The connection
// is pinned to an address that was already screened, so a DNS answer that
// changes between the check and the connect cannot smuggle a private
// target in.
func (f *Fetcher) get(ctx context.Context, target *url.URL, addrs []netip.Addr) (*http.Response, error) {
	transport := f.Transport
	if transport == nil {
		dialer := &net.Dialer{Timeout: dialTimeout}
		transport = &http.Transport{
			// No environment proxy: a proxy would hide the real target.
			Proxy:                 nil,
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: dialTimeout,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, ServerName: target.Hostname()},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				_ = host
				for _, candidate := range addrs {
					conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
					if dialErr == nil {
						return conn, nil
					}
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
				}
				return nil, fmt.Errorf("imageproxy: dial failed")
			},
		}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   f.timeout(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("imageproxy: build request")
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "image/*")
	// No Referer header is sent and no cookie jar is attached: the third
	// party learns nothing about the reader, and no session state travels
	// with the fetch.
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imageproxy: fetch failed")
	}
	return resp, nil
}

// readImage reads and validates the response body.
func readImage(resp *http.Response, maxBytes int64) ([]byte, string, error) {
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, "", fmt.Errorf("imageproxy: bad content type")
	}
	allowed, ok := allowedImageTypes[strings.ToLower(mediaType)]
	if !ok {
		return nil, "", ErrBlocked
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("imageproxy: read body")
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("imageproxy: image too large")
	}
	if len(data) == 0 || !matchesMagic(allowed, data) {
		// The declared type and the bytes disagree: refuse rather than
		// serve content the allowlist did not vet.
		return nil, "", ErrBlocked
	}
	return data, allowed, nil
}

// matchesMagic checks that the payload really is the raster image its
// content type claims.
func matchesMagic(contentType string, data []byte) bool {
	switch contentType {
	case "image/png":
		return len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n"
	case "image/jpeg":
		return len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF
	case "image/gif":
		return len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a")
	case "image/webp":
		return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
	default:
		return false
	}
}

// blockedHost refuses host names that can only resolve inside the
// deployment's own network.
func blockedHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	for _, suffix := range []string{".local", ".internal", ".intranet", ".lan", ".home.arpa", ".localdomain"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	// A single-label name (no dot) only resolves through a local search
	// domain, so it is never a public target.
	return !strings.Contains(host, ".")
}

// isPublicAddr reports whether an address may be dialed from the server.
func isPublicAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsInterfaceLocalMulticast() {
		return false
	}
	if addr.Is4() {
		b := addr.As4()
		switch {
		case b[0] == 0: // 0.0.0.0/8 "this network"
			return false
		case b[0] == 100 && b[1]&0xC0 == 64: // 100.64.0.0/10 carrier NAT
			return false
		case b[0] == 192 && b[1] == 0 && b[2] == 0: // 192.0.0.0/24 IETF protocol assignments
			return false
		case b[0] == 192 && b[1] == 0 && b[2] == 2: // 192.0.2.0/24 TEST-NET-1
			return false
		case b[0] == 198 && (b[1] == 18 || b[1] == 19): // 198.18.0.0/15 benchmarking
			return false
		case b[0] == 198 && b[1] == 51 && b[2] == 100: // 198.51.100.0/24 TEST-NET-2
			return false
		case b[0] == 203 && b[1] == 0 && b[2] == 113: // 203.0.113.0/24 TEST-NET-3
			return false
		case b[0] >= 240: // 240.0.0.0/4 reserved, 255.255.255.255 broadcast
			return false
		}
		return true
	}
	if addr.Is6() {
		b := addr.As16()
		// 2001:db8::/32 documentation range.
		if b[0] == 0x20 && b[1] == 0x01 && b[2] == 0x0D && b[3] == 0xB8 {
			return false
		}
	}
	return true
}
