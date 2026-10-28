// Package mailtransport connects public transport-security observations to the
// existing policy publication. It never submits mail or changes external DNS.
package mailtransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/smtp"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/maildnsreport"
	"github.com/bushixiucai/urmail/internal/store"
)

// Publication converts persisted public values without carrying tenant secrets.
func Publication(row store.DomainTransportSecurity) maildnsreport.Publication {
	return maildnsreport.Publication{Policy: maildnsreport.MTASTSPolicy{ID: row.MtastsPolicyID, Mode: maildnsreport.MTASTSMode(row.MtastsMode), MX: row.PolicyMx, MaxAgeSeconds: int(row.MaxAge)}, RUA: row.TlsRptRua, Enabled: row.Enabled, CacheSafeAfter: row.CacheSafeAfter.Time}
}

// Observer returns fixed status codes only, never arbitrary upstream bodies.
type Observer interface {
	Observe(context.Context, string, string, maildnsreport.Publication) (map[string]bool, error)
}
type observationResolver interface {
	LookupIP(context.Context, string, string) ([]net.IP, error)
	LookupTXT(context.Context, string) ([]string, error)
	LookupMX(context.Context, string) ([]*net.MX, error)
}

// PublicObserver has no runtime bypass for private networks or certificate
// checks. Unexported dependencies let wire tests use an isolated local server.
type PublicObserver struct {
	resolver observationResolver
	dial     func(context.Context, string, string) (net.Conn, error)
	roots    *x509.CertPool
}

func (o PublicObserver) dns() observationResolver {
	if o.resolver != nil {
		return o.resolver
	}
	return net.DefaultResolver
}

var deniedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
}

func publicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range deniedNetworks {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
func (o PublicObserver) dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || (port != "443" && port != "25") {
		return nil, errors.New("TRANSPORT_PROBE_ADDRESS_REJECTED")
	}
	ips, err := o.dns().LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("TRANSPORT_PROBE_DNS_UNAVAILABLE")
	}
	// Reject mixed public/private answers before any connection, preventing
	// rebinding and resolver fallback into loopback, metadata or private hosts.
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, errors.New("TRANSPORT_PROBE_PRIVATE_ADDRESS")
		}
	}
	dialer := net.Dialer{Timeout: 4 * time.Second}
	connect := o.dial
	if connect == nil {
		connect = dialer.DialContext
	}
	for _, ip := range ips {
		conn, e := connect(ctx, network, net.JoinHostPort(ip.String(), port))
		if e == nil {
			return conn, nil
		}
	}
	return nil, errors.New("TRANSPORT_PROBE_CONNECT_FAILED")
}

// Observe verifies one publication using public DNS, HTTPS and SMTP STARTTLS.
func (o PublicObserver) Observe(ctx context.Context, domain, host string, p maildnsreport.Publication) (map[string]bool, error) {
	result := map[string]bool{"dns_id": false, "mx": false, "https_policy": false, "smtp_tls": false}
	txt, err := o.dns().LookupTXT(ctx, "_mta-sts."+domain)
	if err == nil {
		matches := 0
		for _, record := range txt {
			if id, valid := maildnsreport.MTASTSRecordID(record); valid && id == p.Policy.ID {
				matches++
			}
		}
		result["dns_id"] = matches == 1 && len(txt) == 1
	}
	mx, err := o.dns().LookupMX(ctx, domain)
	if err == nil && len(mx) > 0 {
		result["mx"] = true
		found := false
		for _, record := range mx {
			target := strings.TrimSuffix(strings.ToLower(record.Host), ".")
			allowed := false
			for _, wanted := range p.Policy.MX {
				if target == strings.TrimSuffix(wanted, ".") {
					allowed = true
				}
			}
			if !allowed {
				result["mx"] = false
			}
			if target == host {
				found = true
			}
		}
		result["mx"] = result["mx"] && found
	}
	transport := &http.Transport{Proxy: nil, DialContext: o.dialPublic, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: o.roots}, ResponseHeaderTimeout: 4 * time.Second, MaxResponseHeaderBytes: 16 << 10, DisableKeepAlives: true, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("TRANSPORT_PROBE_REDIRECT_REJECTED") }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://mta-sts."+domain+maildnsreport.WellKnownPath, nil)
	if err == nil {
		response, e := client.Do(request)
		if e == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
			_ = response.Body.Close()
			expected, _ := maildnsreport.RenderWellKnown(p.Policy)
			media, params, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
			charset := strings.ToLower(params["charset"])
			result["https_policy"] = response.StatusCode == http.StatusOK && readErr == nil && string(body) == expected && mediaErr == nil && media == "text/plain" && (charset == "" || charset == "utf-8" || charset == "us-ascii")
		}
	}
	conn, err := o.dialPublic(ctx, "tcp", net.JoinHostPort(host, "25"))
	if err == nil {
		deadline := time.Now().Add(5 * time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = conn.SetDeadline(deadline)
		smtpClient, e := smtp.NewClient(conn, host)
		if e == nil {
			if offered, _ := smtpClient.Extension("STARTTLS"); offered {
				e = smtpClient.StartTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, RootCAs: o.roots})
				result["smtp_tls"] = e == nil
			}
			_ = smtpClient.Close()
		} else {
			_ = conn.Close()
		}
	}
	for _, ok := range result {
		if !ok {
			return result, errors.New("TRANSPORT_PUBLIC_OBSERVATION_FAILED")
		}
	}
	return result, nil
}
