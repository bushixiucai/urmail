package mailguard_test

import (
	"net/http"
	"net/netip"
	"testing"

	"github.com/bushixiucai/urmail/internal/mailguard"
)

func mustAddr(t *testing.T, value string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(value)
	if err != nil {
		t.Fatalf("parse %s: %v", value, err)
	}
	return addr
}

func mustPrefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()
	prefixes, err := mailguard.ParseTrustedProxies(values...)
	if err != nil {
		t.Fatalf("ParseTrustedProxies(%v): %v", values, err)
	}
	return prefixes
}

func headers(values ...string) http.Header {
	h := http.Header{}
	for _, value := range values {
		h.Add("X-Forwarded-For", value)
	}
	return h
}

func TestClientSource(t *testing.T) {
	// Trusted proxies used by most cases: loopback (local nginx) plus an
	// internal proxy network.
	trusted := mustPrefixes(t, "127.0.0.1/32, ::1/128, 10.0.0.0/8")

	tests := []struct {
		name       string
		remoteAddr string
		headers    http.Header
		trusted    []netip.Prefix
		wantAddr   string
		wantReason mailguard.SourceReason
	}{
		{
			name:       "direct_peer_ipv4",
			remoteAddr: "203.0.113.10:54321",
			wantAddr:   "203.0.113.10",
			wantReason: mailguard.ReasonPeer,
		},
		{
			name:       "direct_peer_bare_ipv4",
			remoteAddr: "203.0.113.11",
			wantAddr:   "203.0.113.11",
			wantReason: mailguard.ReasonPeer,
		},
		{
			name:       "direct_peer_ipv6",
			remoteAddr: "[2001:db8::5]:443",
			wantAddr:   "2001:db8::5",
			wantReason: mailguard.ReasonPeer,
		},
		{
			name:       "ipv4_mapped_peer_normalized",
			remoteAddr: "[::ffff:203.0.113.12]:1234",
			wantAddr:   "203.0.113.12",
			wantReason: mailguard.ReasonPeer,
		},
		{
			name:       "ipv4_mapped_bare_peer_normalized",
			remoteAddr: "::ffff:203.0.113.13",
			wantAddr:   "203.0.113.13",
			wantReason: mailguard.ReasonPeer,
		},
		{
			name:       "trusted_proxy_single_forwarded_hop",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("198.51.100.7"),
			trusted:    trusted,
			wantAddr:   "198.51.100.7",
			wantReason: mailguard.ReasonForwarded,
		},
		{
			name:       "trusted_proxy_skips_trusted_proxy_hops",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("198.51.100.7, 10.1.2.3"),
			trusted:    trusted,
			wantAddr:   "198.51.100.7",
			wantReason: mailguard.ReasonForwarded,
		},
		{
			name:       "trusted_proxy_multiple_header_lines",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("198.51.100.7", "10.1.2.3"),
			trusted:    trusted,
			wantAddr:   "198.51.100.7",
			wantReason: mailguard.ReasonForwarded,
		},
		{
			name:       "leftmost_forged_value_is_ignored",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("6.6.6.6, 198.51.100.8"),
			trusted:    trusted,
			wantAddr:   "198.51.100.8",
			wantReason: mailguard.ReasonForwarded,
		},
		{
			name:       "leftmost_garbage_is_ignored",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("not-an-ip, 198.51.100.9"),
			trusted:    trusted,
			wantAddr:   "198.51.100.9",
			wantReason: mailguard.ReasonForwarded,
		},
		{
			name:       "forwarded_ipv4_mapped_normalized",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("::ffff:198.51.100.10"),
			trusted:    trusted,
			wantAddr:   "198.51.100.10",
			wantReason: mailguard.ReasonForwarded,
		},
		{
			name:       "forwarded_ipv6",
			remoteAddr: "[::1]:40000",
			headers:    headers("2001:db8::7"),
			trusted:    trusted,
			wantAddr:   "2001:db8::7",
			wantReason: mailguard.ReasonForwarded,
		},
		{
			// A client that reaches the proxy directly can forge any header
			// value, including a public IP. The peer wins, so the attacker
			// cannot spread failures over fresh addresses.
			name:       "forged_header_from_untrusted_peer_is_ignored",
			remoteAddr: "198.51.100.20:5555",
			headers:    headers("1.2.3.4"),
			trusted:    trusted,
			wantAddr:   "198.51.100.20",
			wantReason: mailguard.ReasonPeerForwardedIgnored,
		},
		{
			name:       "malformed_forwarded_value_falls_back_to_peer",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("1.2.3.4:5678"),
			trusted:    trusted,
			wantAddr:   "127.0.0.1",
			wantReason: mailguard.ReasonForwardedMalformed,
		},
		{
			name:       "garbage_forwarded_value_falls_back_to_peer",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("unknown"),
			trusted:    trusted,
			wantAddr:   "127.0.0.1",
			wantReason: mailguard.ReasonForwardedMalformed,
		},
		{
			name:       "rfc7239_for_parameter_is_malformed",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("for=198.51.100.11"),
			trusted:    trusted,
			wantAddr:   "127.0.0.1",
			wantReason: mailguard.ReasonForwardedMalformed,
		},
		{
			name:       "empty_forwarded_value_falls_back_to_peer",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("   "),
			trusted:    trusted,
			wantAddr:   "127.0.0.1",
			wantReason: mailguard.ReasonForwardedMalformed,
		},
		{
			name:       "private_forwarded_address_falls_back_to_peer",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("192.168.1.50"),
			trusted:    trusted,
			wantAddr:   "127.0.0.1",
			wantReason: mailguard.ReasonForwardedNotAllowed,
		},
		{
			name:       "loopback_forwarded_address_falls_back_to_peer",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("127.0.0.9"),
			trusted:    trusted,
			wantAddr:   "127.0.0.1",
			wantReason: mailguard.ReasonForwardedNotAllowed,
		},
		{
			name:       "link_local_forwarded_address_falls_back_to_peer",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("169.254.10.10"),
			trusted:    trusted,
			wantAddr:   "127.0.0.1",
			wantReason: mailguard.ReasonForwardedNotAllowed,
		},
		{
			name:       "unspecified_forwarded_address_falls_back_to_peer",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("0.0.0.0"),
			trusted:    trusted,
			wantAddr:   "127.0.0.1",
			wantReason: mailguard.ReasonForwardedNotAllowed,
		},
		{
			name:       "trusted_proxy_without_header_uses_proxy",
			remoteAddr: "127.0.0.1:40000",
			trusted:    trusted,
			wantAddr:   "127.0.0.1",
			wantReason: mailguard.ReasonTrustedPeerNoForwarded,
		},
		{
			name:       "only_trusted_hops_use_peer",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("10.1.2.3"),
			trusted:    trusted,
			wantAddr:   "127.0.0.1",
			wantReason: mailguard.ReasonForwardedAllTrusted,
		},
		{
			name:       "no_trusted_proxies_configured",
			remoteAddr: "127.0.0.1:40000",
			headers:    headers("198.51.100.12"),
			wantAddr:   "127.0.0.1",
			wantReason: mailguard.ReasonPeerForwardedIgnored,
		},
		{
			name:       "invalid_remote_addr",
			remoteAddr: "definitely-not-an-address",
			headers:    headers("198.51.100.13"),
			trusted:    trusted,
			wantAddr:   "",
			wantReason: mailguard.ReasonPeerInvalid,
		},
		{
			name:       "empty_remote_addr",
			remoteAddr: "   ",
			wantAddr:   "",
			wantReason: mailguard.ReasonPeerInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotAddr, gotReason := mailguard.ClientSource(tc.remoteAddr, tc.headers, tc.trusted)
			if gotReason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", gotReason, tc.wantReason)
			}
			if tc.wantAddr == "" {
				if gotAddr.IsValid() {
					t.Fatalf("address = %v, want invalid", gotAddr)
				}
				return
			}
			if gotAddr != mustAddr(t, tc.wantAddr) {
				t.Fatalf("address = %v, want %v", gotAddr, tc.wantAddr)
			}
		})
	}
}

// TestClientSourceTrustedPrefixForms documents that operators may write the
// same trusted proxy either in IPv4 form or as an IPv4-mapped IPv6 prefix.
func TestClientSourceTrustedPrefixForms(t *testing.T) {
	for _, spec := range []string{"127.0.0.0/8", "::ffff:127.0.0.0/104"} {
		prefixes := mustPrefixes(t, spec)
		addr, reason := mailguard.ClientSource("127.0.0.5:25", headers("198.51.100.30"), prefixes)
		if reason != mailguard.ReasonForwarded {
			t.Fatalf("%s: reason = %q, want forwarded", spec, reason)
		}
		if addr != mustAddr(t, "198.51.100.30") {
			t.Fatalf("%s: address = %v", spec, addr)
		}
	}
}

// TestClientSourceDoesNotTrustXRealIP ensures a header the deployed nginx
// also sets cannot silently replace the verified chain.
func TestClientSourceDoesNotTrustXRealIP(t *testing.T) {
	h := http.Header{}
	h.Set("X-Real-IP", "203.0.113.99")
	addr, reason := mailguard.ClientSource("198.51.100.40:25", h, mustPrefixes(t, "127.0.0.1/32"))
	if reason != mailguard.ReasonPeer || addr != mustAddr(t, "198.51.100.40") {
		t.Fatalf("X-Real-IP influenced the source: %v %q", addr, reason)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		want    []string
		wantErr bool
	}{
		{
			name:   "cidr_and_bare_ip",
			values: []string{"127.0.0.1/32, ::1/128", "10.0.0.1"},
			want:   []string{"127.0.0.1/32", "::1/128", "10.0.0.1/32"},
		},
		{
			name:   "cidr_is_masked",
			values: []string{"10.1.2.3/8"},
			want:   []string{"10.0.0.0/8"},
		},
		{
			name:   "ipv4_mapped_prefix_normalized",
			values: []string{"::ffff:192.0.2.0/120"},
			want:   []string{"192.0.2.0/24"},
		},
		{
			name:    "invalid_cidr",
			values:  []string{"10.0.0.0/99"},
			wantErr: true,
		},
		{
			name:    "invalid_ip",
			values:  []string{"proxy.internal"},
			wantErr: true,
		},
		{
			name:   "empty_input",
			values: []string{"", "   "},
			want:   nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mailguard.ParseTrustedProxies(tc.values...)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("prefixes = %v, want %v", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i] != netip.MustParsePrefix(want) {
					t.Fatalf("prefix[%d] = %v, want %v", i, got[i], want)
				}
			}
		})
	}
}
